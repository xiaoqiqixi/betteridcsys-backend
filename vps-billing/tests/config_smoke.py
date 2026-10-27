#!/usr/bin/env python3
"""Fail-closed production startup checks; no DB or network dependency required."""
import argparse
import secrets
import os
import subprocess
import unittest

parser = argparse.ArgumentParser()
parser.add_argument('--binary', required=True)
ARGS, rest = parser.parse_known_args()

class ProductionConfig(unittest.TestCase):
    def check_fails(self, settings, expected):
        env = {k: v for k, v in os.environ.items() if not k.startswith(('APP_', 'SESSION_SECRET', 'MYSQL_', 'BOOTSTRAP_', 'TRUSTED_PROXY', 'PAYMENT_PROVIDER', 'EMAIL_PROVIDER', 'SMTP_', 'PLUGIN_', 'UPDATE_', 'INTEGRATIONS_CONFIG'))}
        env.update(settings)
        completed = subprocess.run([ARGS.binary], env=env, text=True, capture_output=True, timeout=15)
        self.assertNotEqual(completed.returncode, 0, 'Unsafe configuration started successfully')
        self.assertIn(expected, completed.stderr)
        return completed

    def test_default_is_not_demo(self):
        self.check_fails({}, 'SESSION_SECRET')

    def test_demo_like_secret_rejected(self):
        self.check_fails({'SESSION_SECRET': 'example-secret-not-for-production-1234567'}, 'SESSION_SECRET')

    def test_plaintext_origin_rejected(self):
        self.check_fails({'SESSION_SECRET': secrets.token_hex(32), 'APP_ORIGIN': 'http://billing.invalid'}, 'APP_ORIGIN')

    def test_missing_database_rejected(self):
        self.check_fails({'SESSION_SECRET': secrets.token_hex(32), 'APP_ORIGIN': 'https://billing.invalid'}, 'MYSQL_DSN')

    def test_unknown_mode_rejected(self):
        self.check_fails({'APP_MODE': 'staging-typo'}, 'APP_MODE')

    def test_ambiguous_origin_rejected(self):
        for origin in ('https://billing.invalid?', 'https://:443'):
            with self.subTest(origin=origin):
                self.check_fails({'SESSION_SECRET': secrets.token_hex(32), 'APP_ORIGIN': origin}, 'APP_ORIGIN')

    def test_unknown_payment_provider_rejected(self):
        self.check_fails({'APP_MODE': 'demo', 'PAYMENT_PROVIDER': 'payment.not-installed'}, 'integration configuration')

    def test_unknown_email_provider_rejected(self):
        self.check_fails({'APP_MODE': 'demo', 'EMAIL_PROVIDER': 'email.not-installed'}, 'integration configuration')

    def test_invalid_smtp_key_path_rejected_without_disclosing_path(self):
        completed = self.check_fails({'APP_MODE': 'demo', 'SMTP_ENCRYPTION_KEY_FILE': 'relative-invalid-key'}, 'SMTP encryption key')
        self.assertNotIn('relative-invalid-key', completed.stdout + completed.stderr)

if __name__ == '__main__':
    unittest.main(argv=['config_smoke.py'] + rest, verbosity=2)
