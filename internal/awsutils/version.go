package awsutils

import "fmt"

// eksMinor parses an EKS version like "1.33" or "1.33.x" and returns the minor (e.g., 33).
func eksMinor(version string) (int, error) {
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil {
		return 0, fmt.Errorf("parse eks version %q: %w", version, err)
	}
	return minor, nil
}
