// Package decimal owns the exact fixed-point Decimal transport contract used
// by visualization validation and runtime reductions.
package decimal

import (
	"fmt"
	"math/big"
)

// Parse validates canonical fixed-point Decimal transport and returns its
// exact rational value together with the source fractional scale.
func Parse(token string) (*big.Rat, int, error) {
	if err := Validate(token); err != nil {
		return nil, 0, err
	}
	start := 0
	if token[0] == '-' {
		start = 1
	}
	dot := -1
	for index := start; index < len(token); index++ {
		if token[index] == '.' {
			dot = index
			break
		}
	}
	scale := 0
	if dot >= 0 {
		scale = len(token) - dot - 1
	}
	rational, ok := new(big.Rat).SetString(token)
	if !ok {
		return nil, 0, fmt.Errorf("numeric literal %q must use canonical fixed-point notation", token)
	}
	return rational, scale, nil
}

// Validate checks canonical fixed-point spelling without allocating a
// rational. It is used on hot renderer validation paths.
func Validate(token string) error {
	if token == "" {
		return fmt.Errorf("numeric literal is empty")
	}
	start := 0
	negative := false
	switch token[0] {
	case '-':
		negative = true
		start = 1
	case '+':
		return fmt.Errorf("numeric literal %q must not use a leading plus", token)
	}
	if start == len(token) {
		return fmt.Errorf("numeric literal %q has no digits", token)
	}
	dot := -1
	digitCount := 0
	for index := start; index < len(token); index++ {
		char := token[index]
		switch {
		case char >= '0' && char <= '9':
			digitCount++
		case char == '.' && dot < 0:
			dot = index
		default:
			return fmt.Errorf("numeric literal %q must use canonical fixed-point notation", token)
		}
	}
	if digitCount == 0 || dot == start || dot == len(token)-1 {
		return fmt.Errorf("numeric literal %q must use canonical fixed-point notation", token)
	}
	integerEnd := len(token)
	if dot >= 0 {
		integerEnd = dot
	}
	integerDigits := token[start:integerEnd]
	if len(integerDigits) > 1 && integerDigits[0] == '0' {
		return fmt.Errorf("numeric literal %q must not have leading zeroes", token)
	}
	if negative {
		zero := true
		for index := start; index < len(token); index++ {
			if token[index] != '0' && token[index] != '.' {
				zero = false
				break
			}
		}
		if zero {
			return fmt.Errorf("numeric literal %q must not be negative zero", token)
		}
	}
	return nil
}
