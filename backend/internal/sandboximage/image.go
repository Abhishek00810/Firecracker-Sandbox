package sandboximage

import (
	"fmt"
	"regexp"
	"strings"
)

const Default = "alpine"

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Normalize returns the stable image identifier carried across all planes.
// An omitted image preserves the existing Alpine behavior.
func Normalize(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return Default, nil
	}
	if !idPattern.MatchString(value) {
		return "", fmt.Errorf("invalid image %q", value)
	}
	return value, nil
}

func PoolKey(image, sizeKey string) string {
	return image + ":" + sizeKey
}
