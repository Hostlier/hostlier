package config

import("math";"regexp";"strconv";"hostlier/internal/model")

// ParseMemory converts a supported binary quantity to a positive signed byte count.
func ParseMemory(v string) (int64, error) {
	m := regexp.MustCompile(`^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti|K|M|G|T)?$`).FindStringSubmatch(v)
	// Reject quantities outside the supported integer-and-unit syntax.
	if m == nil {
		return 0, model.Err("MEMORY", "Invalid memory size")
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"": 1, "Ki": 1024, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12}
	value := n * mult[m[2]]
	// Guard finite positive signed-byte bounds before converting to int64.
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value >= math.Exp2(63) {
		return 0, model.Err("MEMORY", "Memory size must be positive and fit int64")
	}
	return int64(value), nil
}
