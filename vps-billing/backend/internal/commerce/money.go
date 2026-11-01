package commerce

import (
	"errors"
	"math/big"
	"time"

	api "aster/billing/internal/financeapi"
)

const MaxMinor int64 = 9007199254740991

var ErrMoney = errors.New("invalid or overflowing money")
var ErrInput = errors.New("invalid commerce input")

func Money(currency string, decimals, amount int64) api.Money {
	return api.Money{Currency: currency, DecimalPlaces: decimals, AmountMinor: amount}
}

// Add and Multiply keep every intermediate value inside the exact browser
// integer range. No currency conversion or floating point arithmetic occurs.
func Add(a, b int64) (int64, error) {
	if a < -MaxMinor || a > MaxMinor || b < -MaxMinor || b > MaxMinor || a+b < -MaxMinor || a+b > MaxMinor {
		return 0, ErrMoney
	}
	return a + b, nil
}

func Multiply(amount, quantity int64) (int64, error) {
	if amount < 0 || amount > MaxMinor || quantity < 0 || quantity > MaxMinor || quantity != 0 && amount > MaxMinor/quantity {
		return 0, ErrMoney
	}
	return amount * quantity, nil
}

// FloorRatio uses mathematical floor, including negative downgrade amounts.
// big.Int avoids overflow before division when amounts approach MaxMinor.
func FloorRatio(amount, numerator, denominator int64) (int64, error) {
	if amount < -MaxMinor || amount > MaxMinor || numerator < 0 || denominator <= 0 {
		return 0, ErrMoney
	}
	n := new(big.Int).Mul(big.NewInt(amount), big.NewInt(numerator))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, big.NewInt(denominator), r)
	if n.Sign() < 0 && r.Sign() != 0 {
		q.Sub(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Int64() < -MaxMinor || q.Int64() > MaxMinor {
		return 0, ErrMoney
	}
	return q.Int64(), nil
}

func Discount(amount int64, kind string, basisPoints, fixed int64) (int64, error) {
	if amount < 0 || amount > MaxMinor {
		return 0, ErrMoney
	}
	switch kind {
	case "percent":
		if basisPoints < 1 || basisPoints > 10000 {
			return 0, ErrInput
		}
		return FloorRatio(amount, basisPoints, 10000)
	case "fixed":
		if fixed < 1 || fixed > MaxMinor {
			return 0, ErrInput
		}
		return min(amount, fixed), nil
	}
	return 0, ErrInput
}

func CycleMonths(cycle string) (int, error) {
	switch cycle {
	case "monthly":
		return 1, nil
	case "quarterly":
		return 3, nil
	case "semiannually":
		return 6, nil
	case "annually":
		return 12, nil
	case "biennially":
		return 24, nil
	case "triennially":
		return 36, nil
	}
	return 0, ErrInput
}

// CalendarMonths clamps to the last day of the target month. Retaining the
// original anchor day across renewals makes Jan 31 -> Feb 28 -> Mar 31.
func CalendarMonths(at time.Time, months, anchorDay int) time.Time {
	if anchorDay < 1 || anchorDay > 31 {
		anchorDay = at.Day()
	}
	first := time.Date(at.Year(), at.Month()+time.Month(months), 1, at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), at.Location())
	lastDay := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(anchorDay, lastDay)-1)
}

func Prorate(oldPrice, newPrice int64, cycle string, due, now time.Time) (amount, days int64, err error) {
	if oldPrice < 0 || newPrice < 0 || oldPrice > MaxMinor || newPrice > MaxMinor {
		return 0, 0, ErrMoney
	}
	months, err := CycleMonths(cycle)
	if err != nil {
		return 0, 0, err
	}
	if !due.After(now) {
		return 0, 0, nil
	}
	const day = 24 * time.Hour
	remaining := due.Sub(now)
	days = int64(remaining / day)
	if remaining%day != 0 {
		days++
	}
	periodDays := int64(due.Sub(CalendarMonths(due, -months, due.Day())) / day)
	amount, err = FloorRatio(newPrice-oldPrice, days, periodDays)
	return
}

// BalanceDue counts credit and external receipts independently. Refunds are
// tracked separately and never reopen a paid invoice for another collection.
func BalanceDue(total, paid, credit int64) (int64, error) {
	if total < 0 || paid < 0 || credit < 0 || total > MaxMinor || paid > MaxMinor || credit > MaxMinor || paid > total || credit > total-paid {
		return 0, ErrMoney
	}
	return total - paid - credit, nil
}

func ApplyCredit(total, paid, applied, balance int64) (used, remaining int64, err error) {
	due, err := BalanceDue(total, paid, applied)
	if err != nil || balance < 0 || balance > MaxMinor {
		return 0, 0, ErrMoney
	}
	used = min(due, balance)
	return used, balance - used, nil
}

func Refundable(payment, refunded, requested int64) error {
	if payment < 0 || payment > MaxMinor || refunded < 0 || refunded > payment || requested <= 0 || requested > payment-refunded {
		return ErrMoney
	}
	return nil
}
