package report

import "fmt"

// Money formats cents as a decimal amount.
func Money(cents int) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
