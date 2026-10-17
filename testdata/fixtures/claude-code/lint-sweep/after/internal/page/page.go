package page

// Pages returns how many pages of size items are needed for total items.
func Pages(total, size int) int {
	if size <= 0 {
		return 0
	}
	return (total + size - 1) / size
}
