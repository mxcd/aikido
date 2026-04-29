package cli

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// LoadDotEnv reads simple KEY=VALUE lines from path and sets each key in the
// process environment if it isn't already set. Lines starting with '#' and
// blank lines are skipped. Quoted values ("..." or '...') are unwrapped. A
// missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 1 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = unquote(val)
		if _, set := os.LookupEnv(key); set {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err()
}

func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
