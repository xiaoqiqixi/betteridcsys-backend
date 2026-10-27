#!/usr/bin/env python3
"""HTTP-only security regression suite. Uses an explicitly started local demo API.
Run: python3 tests/security_smoke.py --base http://127.0.0.1:8080
No real payments, destructive account changes, or external targets.
"""
import argparse
import concurrent.futures
import http.cookiejar
import json
import os
import unittest
import urllib.error
import urllib.parse
import urllib.request
import uuid

PARSER = argparse.ArgumentParser()
PARSER.add_argument('--base', default=os.getenv('TEST_BASE_URL', 'http://127.0.0.1:8080'))
PARSER.add_argument('--origin', default=os.getenv('TEST_ORIGIN', ''))
PARSER.add_argument('--customer-email', default=os.getenv('TEST_CUSTOMER_EMAIL', 'demo@aster.local'))
PARSER.add_argument('--customer-password', default=os.getenv('TEST_CUSTOMER_PASSWORD', ''))
PARSER.add_argument('--admin-email', default=os.getenv('TEST_ADMIN_EMAIL', 'admin@aster.local'))
PARSER.add_argument('--admin-password', default=os.getenv('TEST_ADMIN_PASSWORD', ''))
PARSER.add_argument('--second-email', default=os.getenv('TEST_SECOND_EMAIL', ''))
PARSER.add_argument('--second-password', default=os.getenv('TEST_SECOND_PASSWORD', ''))
PARSER.add_argument('--expect-fresh-cny', action='store_true')
ARGS, REMAINING = PARSER.parse_known_args()
if urllib.parse.urlparse(ARGS.base).hostname not in ('localhost', '127.0.0.1', '::1'):
    PARSER.error('Only explicit loopback/local servers are permitted')

class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ''

    def request(self, path, method='GET', body=None, csrf=True, headers=None):
        hdr = {'Accept': 'application/json'}
        if body is not None:
            hdr['Content-Type'] = 'application/json'
        if method != 'GET' and csrf:
            hdr['X-CSRF-Token'] = self.csrf
            hdr['Origin'] = ARGS.origin or ARGS.base
        hdr.update(headers or {})
        req = urllib.request.Request(ARGS.base + path, data=json.dumps(body).encode() if body is not None else None,
                                     method=method, headers=hdr)
        try:
            resp = self.opener.open(req, timeout=10)
        except urllib.error.HTTPError as error:
            resp = error
        raw = resp.read()
        try:
            payload = json.loads(raw)
        except ValueError:
            payload = {'raw': raw.decode(errors='replace')}
        return resp.status, payload, resp.headers

    def session(self):
        status, data, headers = self.request('/api/v1/session')
        assert status == 200, (status, data)
        self.csrf = data['data']['csrf_token']
        return data['data'], headers

    def login(self, email, password):
        self.session()
        result = self.request('/api/v1/auth/login', 'POST', {'email': email, 'password': password})
        assert result[0] == 200, result[:2]
        self.session()
        return self

FINANCIAL_FIELDS = ('code', 'name', 'prefix', 'suffix', 'decimal_places', 'enabled', 'sort_order')


def currency_update(config, default=None):
    return {'revision': config['revision'], 'default_currency': default or config['default_currency'],
            'currencies': [{key: row[key] for key in FINANCIAL_FIELDS} for row in config['currencies']]}


def prepare_financial_fixture(admin, expect_fresh=False):
    """Approve synthetic currencies and independent prices through the real API."""
    status, payload, _ = admin.request('/api/v1/admin/currencies')
    assert status == 200, 'Financial fixture registry read failed'
    config = payload['data']
    assert config['default_currency'] == 'CNY', 'Fresh/restore fixture default changed'
    if expect_fresh:
        assert len(config['currencies']) == 1 and config['currencies'][0]['code'] == 'CNY', 'Fresh database is not CNY-only'
        assert config['currencies'][0]['enabled'] and config['currencies'][0]['decimal_places'] == 2
        assert admin.request('/api/v1/plans?currency=USD')[0] == 400, 'Fresh database exposes unapproved USD prices'
    requested = (
        {'code': 'USD', 'name': 'Synthetic USD', 'prefix': '$', 'suffix': '', 'decimal_places': 2, 'enabled': True, 'sort_order': 10},
        {'code': 'JPY', 'name': 'Synthetic JPY', 'prefix': '¥', 'suffix': '', 'decimal_places': 0, 'enabled': True, 'sort_order': 20},
        {'code': 'KWD', 'name': 'Synthetic KWD', 'prefix': '', 'suffix': ' KWD', 'decimal_places': 3, 'enabled': True, 'sort_order': 30},
    )
    body = currency_update(config)
    existing = {row['code'] for row in body['currencies']}
    additions = [row for row in requested if row['code'] not in existing]
    if additions:
        body['currencies'].extend(additions)
        assert admin.request('/api/v1/admin/currencies', 'POST', body)[0] == 200, 'Explicit currency approval failed'
    prices = {'USD': ('6.00', 600), 'JPY': ('1250', 1250), 'KWD': ('1.234', 1234)}
    for code, (amount, minor) in prices.items():
        status, payload, _ = admin.request('/api/v1/admin/plan-prices?currency=' + code)
        assert status == 200, 'Independent price fixture read failed'
        catalog = payload['data']
        if expect_fresh:
            assert all(row['amount'] == '' and not row['available'] for row in catalog['plans']), 'New currency was given implicit converted prices'
        body = {'revision': catalog['revision'], 'currency': code, 'prices': [
            {'plan_id': row['plan_id'], 'amount': amount, 'available': True, 'plan_available': True}
            for row in catalog['plans']]}
        assert body['prices'], 'Fixture contains no plans'
        status, payload, _ = admin.request('/api/v1/admin/plan-prices', 'POST', body)
        assert status == 200, 'Explicit independent price fixture write failed'
        assert all(row['amount_minor'] == minor for row in payload['data']['plans']), 'Price fixture was rounded or converted'
    return {'fresh_cny_only_verified': expect_fresh, 'registered': ['USD', 'JPY', 'KWD'],
            'independent_minor_amounts': {code: values[1] for code, values in prices.items()}, 'automatic_fx': False}


class SecuritySmoke(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.financial_fixture = None
        if ARGS.admin_password:
            cls.financial_fixture = prepare_financial_fixture(Client().login(ARGS.admin_email, ARGS.admin_password), ARGS.expect_fresh_cny)

    def setUp(self):
        self.client = Client()

    def customer(self):
        if not ARGS.customer_password:
            self.skipTest('TEST_CUSTOMER_PASSWORD not supplied')
        return Client().login(ARGS.customer_email, ARGS.customer_password)

    def test_unauthenticated_data_and_admin_denied(self):
        for path in ('overview', 'services', 'orders', 'invoices', 'audit', 'admin/overview',
                     'admin/customers', 'admin/orders', 'admin/invoices', 'admin/integrations', 'admin/site-settings',
                     'admin/email-settings', 'admin/currencies', 'admin/plan-prices'):
            with self.subTest(path=path):
                self.assertIn(self.client.request('/api/v1/' + path)[0], (401, 403))

    def test_login_requires_csrf(self):
        self.client.session()
        for token in (None, 'wrong-token'):
            kwargs = {'csrf': False} if token is None else {'headers': {'X-CSRF-Token': token}}
            status, _, _ = self.client.request('/api/v1/auth/login', 'POST',
                {'email': ARGS.customer_email, 'password': ARGS.customer_password}, **kwargs)
            self.assertIn(status, (400, 403))

    def test_customer_admin_boundary(self):
        client = self.customer()
        for path in ('overview', 'customers', 'orders', 'invoices', 'integrations', 'site-settings', 'email-settings', 'currencies', 'plan-prices'):
            with self.subTest(path=path):
                self.assertEqual(client.request('/api/v1/admin/' + path)[0], 403)

    def test_origin_and_csrf_on_order(self):
        client = self.customer()
        for headers, csrf in (({'Origin': 'https://attacker.invalid'}, True), ({}, False),
                              ({'X-CSRF-Token': 'wrong'}, True)):
            with self.subTest(headers=headers, csrf=csrf):
                self.assertIn(client.request('/api/v1/orders', 'POST', {'plan_id': 'invalid', 'hostname': 'smoke'},
                    csrf=csrf, headers=headers)[0], (400, 403))

    def test_order_idempotency_price_checkout_and_ownership(self):
        client = self.customer()
        status, payload, _ = client.request('/api/v1/plans')
        self.assertEqual(status, 200)
        plans = [p for p in payload['data'] if p['available']]
        self.assertTrue(plans)
        plan = plans[0]
        key = str(uuid.uuid4())
        body = {'plan_id': plan['id'], 'hostname': 'smoke-' + key[:8], 'currency': plan['currency']}
        headers = {'Idempotency-Key': key}
        status, created, _ = client.request('/api/v1/orders', 'POST', body, headers=headers)
        self.assertIn(status, (200, 201), created)
        order = created['data']
        self.assertEqual(order['total_minor'], plan['price_minor'])
        status, replayed, _ = client.request('/api/v1/orders', 'POST', body, headers=headers)
        self.assertIn(status, (200, 201), replayed)
        self.assertEqual(replayed['data']['id'], order['id'])
        changed = dict(body, hostname=body['hostname'] + '-other')
        self.assertIn(client.request('/api/v1/orders', 'POST', changed, headers=headers)[0], (409, 422))
        status, arbitrary, _ = client.request('/api/v1/orders', 'POST', dict(body, amount_minor=1, total_minor=1),
            headers={'Idempotency-Key': str(uuid.uuid4())})
        if status in (200, 201):
            self.assertEqual(arbitrary['data']['total_minor'], plan['price_minor'])
        else:
            self.assertIn(status, (400, 422))
        status, payload, _ = client.request('/api/v1/invoices')
        self.assertEqual(status, 200)
        invoice = next(i for i in payload['data'] if i['order_id'] == order['id'])
        self.assertNotEqual(invoice['status'], 'paid')
        path = '/api/v1/invoices/' + invoice['id']
        self.assertEqual(client.request(path + '/checkout', 'POST', {})[0], 503)
        status, after, _ = client.request(path)
        self.assertEqual(status, 200)
        self.assertEqual(after['data']['status'], invoice['status'])
        self.assertFalse(after['data'].get('paid_at'))
        if ARGS.second_email and ARGS.second_password:
            second = Client().login(ARGS.second_email, ARGS.second_password)
            self.assertIn(second.request(path)[0], (403, 404))
            self.assertIn(second.request(path + '/checkout', 'POST', {})[0], (403, 404))
        else:
            print('NOTE: cross-customer ownership not exercised: no second account supplied')

    def test_logout_invalidates_cookie(self):
        client = self.customer()
        cookies = '; '.join(c.name + '=' + c.value for c in client.jar)
        status, _, _ = client.request('/api/v1/auth/logout', 'POST', {})
        self.assertIn(status, (200, 204))
        self.assertIn(client.request('/api/v1/overview')[0], (401, 403))
        fresh = Client()
        self.assertIn(fresh.request('/api/v1/overview', headers={'Cookie': cookies})[0], (401, 403))

    def test_session_cookie_and_login_rotation(self):
        if not ARGS.customer_password:
            self.skipTest('TEST_CUSTOMER_PASSWORD not supplied')
        anon = Client()
        _, headers = anon.session()
        cookie = headers.get('Set-Cookie', '')
        self.assertIn('HttpOnly', cookie)
        self.assertIn('SameSite=Strict', cookie)
        old = '; '.join(c.name + '=' + c.value for c in anon.jar)
        status, _, _ = anon.request('/api/v1/auth/login', 'POST',
            {'email': ARGS.customer_email, 'password': ARGS.customer_password})
        self.assertEqual(status, 200)
        current = '; '.join(c.name + '=' + c.value for c in anon.jar)
        self.assertNotEqual(old, current)
        stale = Client()
        self.assertIn(stale.request('/api/v1/overview', headers={'Cookie': old})[0], (401, 403))

    def test_admin_read_access(self):
        if not ARGS.admin_password:
            self.skipTest('TEST_ADMIN_PASSWORD not supplied')
        admin = Client().login(ARGS.admin_email, ARGS.admin_password)
        for path in ('overview', 'customers', 'orders', 'invoices', 'integrations', 'site-settings', 'email-settings'):
            with self.subTest(path=path):
                status, payload, _ = admin.request('/api/v1/admin/' + path)
                self.assertEqual(status, 200, payload)
                self.assertNotIn('password_hash', json.dumps(payload))

    def test_integration_manifest_is_admin_only_readonly_and_redacted(self):
        if not ARGS.admin_password:
            self.skipTest('TEST_ADMIN_PASSWORD not supplied')
        admin = Client().login(ARGS.admin_email, ARGS.admin_password)
        status, payload, _ = admin.request('/api/v1/admin/integrations')
        self.assertEqual(status, 200, payload)
        items = payload['data']['items']
        self.assertTrue(items)
        self.assertEqual(len({i['id'] for i in items}), len(items))
        for item in items:
            self.assertIn(item['kind'], ('payment', 'email'))
            self.assertIn(item['status'], ('disabled', 'ready', 'unavailable'))
            expected_mode = 'admin_api' if item['id'] == 'email.smtp' else 'environment'
            self.assertEqual(item['configuration_mode'], expected_mode)
            self.assertNotIn('configuration_values', item)
            self.assertNotIn('config', item)
            for field in item['configuration_fields']:
                self.assertNotIn('value', field)
                self.assertNotIn('default', field)
        self.assertTrue(all(not i['enabled'] for i in items), 'Default demo must not enable live providers')
        status, _, _ = admin.request('/api/v1/admin/integrations', 'POST', {'enabled': True})
        self.assertIn(status, (404, 405))

    def test_concurrent_duplicate_orders(self):
        client = self.customer()
        _, payload, _ = client.request('/api/v1/plans')
        plan = next(p for p in payload['data'] if p['available'])
        key = str(uuid.uuid4())
        cookies = '; '.join(c.name + '=' + c.value for c in client.jar)
        body = {'plan_id': plan['id'], 'hostname': 'parallel-' + key[:8], 'currency': plan['currency']}
        def submit(_):
            sender = Client()
            sender.csrf = client.csrf
            return sender.request('/api/v1/orders', 'POST', body,
                headers={'Cookie': cookies, 'Idempotency-Key': key})
        with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
            results = list(pool.map(submit, range(6)))
        for status, data, _ in results:
            self.assertIn(status, (200, 201), data)
        self.assertEqual(len({data['data']['id'] for _, data, _ in results}), 1)

    def test_unknown_host_rejected(self):
        status, _, _ = self.client.request('/api/v1/session', headers={'Host': 'attacker.invalid'})
        self.assertIn(status, (400, 403, 421))

    def test_currency_validation_and_language_are_independent(self):
        status, payload, _ = self.client.request('/api/v1/site-settings')
        self.assertEqual(status, 200)
        self.assertIn(payload['data']['default_locale'], ('zh-CN', 'en'))
        self.assertIn(payload['data']['default_currency'], ('CNY', 'USD'))
        for private in ('email_signature', 'invoice_address', 'revision'):
            self.assertNotIn(private, payload['data'])
        for query in ('currency=EUR', 'currency=', 'currency=CNY&currency=USD'):
            status, payload, _ = self.client.request('/api/v1/plans?' + query)
            self.assertEqual(status, 400, payload)
            self.assertIn(payload['error']['code'], ('INVALID_CURRENCY', 'UNSUPPORTED_CURRENCY'))
        for currency in ('CNY', 'USD'):
            status, payload, _ = self.client.request('/api/v1/plans?currency=' + currency,
                headers={'Accept-Language': 'en'})
            self.assertEqual(status, 200, payload)
            self.assertTrue(all(plan['currency'] == currency for plan in payload['data']))
            self.assertTrue(all(isinstance(plan['price_minor'], int) for plan in payload['data']))
        status, payload, _ = self.client.request('/api/v1/orders', headers={'Accept-Language': 'en'})
        self.assertEqual(status, 401)
        self.assertEqual(payload['error']['code'], 'AUTH_REQUIRED')
        self.assertTrue(payload['error']['message'].isascii(), payload)

    def test_settings_and_historical_multicurrency_invoices(self):
        if not ARGS.admin_password:
            self.skipTest('TEST_ADMIN_PASSWORD not supplied')
        admin = Client().login(ARGS.admin_email, ARGS.admin_password)
        customer = self.customer()
        status, payload, _ = admin.request('/api/v1/admin/site-settings')
        self.assertEqual(status, 200, payload)
        original = payload['data']
        _, financial, _ = admin.request('/api/v1/admin/currencies')
        original_default = financial['data']['default_currency']
        for update in ({'support_url': 'javascript:alert(1)'}, {'logo': 'https://example.invalid/logo.svg'},
                       {'theme': 'body{display:none}'}, {'default_currency': 'EUR'}, {'default_locale': 'fr'}):
            status, _, _ = admin.request('/api/v1/admin/site-settings', 'POST', dict(original, **update))
            self.assertEqual(status, 400)
        status, _, _ = customer.request('/api/v1/admin/site-settings', 'POST', original)
        self.assertEqual(status, 403)
        status, _, _ = admin.request('/api/v1/admin/site-settings', 'POST', original, csrf=False)
        self.assertEqual(status, 403)
        issued_by = '测试开票主体-' + uuid.uuid4().hex[:8]
        status, payload, _ = admin.request('/api/v1/admin/site-settings', 'POST',
            dict(original, site_name='测试品牌', invoice_issuer=issued_by))
        self.assertEqual(status, 200, payload)
        try:
            self.assertEqual(admin.request('/api/v1/admin/site-settings', 'POST', original)[0], 409)
            snapshots = []
            for currency in ('CNY', 'USD'):
                _, catalog, _ = customer.request('/api/v1/plans?currency=' + currency)
                plan = next(plan for plan in catalog['data'] if plan['available'])
                _, before, _ = customer.request('/api/v1/overview?currency=' + currency)
                key = str(uuid.uuid4())
                body = {'plan_id': plan['id'], 'hostname': 'snapshot-' + key[:8], 'currency': currency}
                status, payload, _ = customer.request('/api/v1/orders', 'POST', body,
                    headers={'Idempotency-Key': key})
                self.assertEqual(status, 201, payload)
                order = payload['data']
                self.assertEqual((order['currency'], order['total_minor']), (currency, plan['price_minor']))
                other = 'USD' if currency == 'CNY' else 'CNY'
                self.assertEqual(customer.request('/api/v1/orders', 'POST', dict(body, currency=other),
                    headers={'Idempotency-Key': key})[0], 409)
                _, after, _ = customer.request('/api/v1/overview?currency=' + currency)
                self.assertEqual(after['data']['currency'], currency)
                self.assertEqual(after['data']['pending_invoices'], before['data']['pending_invoices'] + 1)
                _, payload, _ = customer.request('/api/v1/invoices')
                invoice = next(i for i in payload['data'] if i['order_id'] == order['id'])
                self.assertEqual(invoice['issuer']['name'], issued_by)
                self.assertEqual((invoice['currency'], invoice['amount_minor']), (currency, plan['price_minor']))
                snapshots.append(invoice)
            _, current, _ = admin.request('/api/v1/admin/site-settings')
            self.assertEqual(admin.request('/api/v1/admin/site-settings', 'POST',
                dict(current['data'], invoice_issuer='后来主体'))[0], 200)
            _, financial, _ = admin.request('/api/v1/admin/currencies')
            self.assertEqual(admin.request('/api/v1/admin/currencies', 'POST',
                currency_update(financial['data'], 'USD'))[0], 200)
            for expected in snapshots:
                status, detail, _ = customer.request('/api/v1/invoices/' + expected['id'])
                self.assertEqual(status, 200)
                self.assertEqual(detail['data'], expected)
                self.assertEqual(admin.request('/api/v1/admin/invoices/' + expected['id'])[0], 200)
                self.assertEqual(admin.request('/api/v1/admin/invoices/' + expected['id'] + '/checkout', 'POST', {})[0], 405)
        finally:
            _, financial, _ = admin.request('/api/v1/admin/currencies')
            self.assertEqual(admin.request('/api/v1/admin/currencies', 'POST',
                currency_update(financial['data'], original_default))[0], 200)
            _, current, _ = admin.request('/api/v1/admin/site-settings')
            restore = dict(original, revision=current['data']['revision'])
            self.assertEqual(admin.request('/api/v1/admin/site-settings', 'POST', restore)[0], 200)

    def test_financial_registry_precision_no_fx_and_write_guards(self):
        if not ARGS.admin_password:
            self.skipTest('TEST_ADMIN_PASSWORD not supplied')
        admin = Client().login(ARGS.admin_email, ARGS.admin_password)
        customer = self.customer()
        _, configured, _ = admin.request('/api/v1/admin/currencies')
        body = currency_update(configured['data'])
        self.assertEqual(admin.request('/api/v1/admin/currencies', 'POST', body, csrf=False)[0], 403)
        self.assertEqual(customer.request('/api/v1/admin/currencies', 'POST', body)[0], 403)
        changed = currency_update(configured['data'])
        next(row for row in changed['currencies'] if row['code'] == 'USD')['decimal_places'] = 3
        status, failure, _ = admin.request('/api/v1/admin/currencies', 'POST', changed)
        self.assertEqual(status, 409)
        self.assertEqual(failure['error']['code'], 'CURRENCY_PRECISION_LOCKED')
        for code, decimals, minor in (('JPY', 0, 1250), ('KWD', 3, 1234)):
            _, catalog, _ = customer.request('/api/v1/plans?currency=' + code)
            plan = next(row for row in catalog['data'] if row['available'])
            self.assertEqual(plan['price_minor'], minor)
            key = str(uuid.uuid4())
            status, created, _ = customer.request('/api/v1/orders', 'POST', {
                'plan_id': plan['id'], 'hostname': 'precision-' + key[:8], 'currency': code},
                headers={'Idempotency-Key': key})
            self.assertEqual(status, 201)
            order = created['data']
            self.assertEqual((order['currency'], order['total_minor'], order['currency_format']['decimal_places']),
                             (code, minor, decimals))
            _, invoices, _ = customer.request('/api/v1/invoices')
            invoice = next(row for row in invoices['data'] if row['order_id'] == order['id'])
            self.assertEqual((invoice['currency'], invoice['amount_minor'], invoice['currency_format']['decimal_places']),
                             (code, minor, decimals))
            self.assertNotEqual(invoice['status'], 'paid')

if __name__ == '__main__':
    unittest.main(argv=['security_smoke.py'] + REMAINING, verbosity=2)
