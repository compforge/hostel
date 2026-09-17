package manager

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/qiankunli/go-stdx/shellx"
	hostel "github.com/qiankunli/hostel/internal"
)

// SessionSettings applies trusted environment updates and a caller's directory
// before execution, under the shell's run lock. Directory expands only variable
// references and a leading ~, never shell commands or expressions.
type SessionSettings struct {
	Directory   string
	Environment map[string]string
}

func (s *Shell) prepareSessionLocked(ctx context.Context, settings *SessionSettings) (string, error) {
	if err := ValidateRequestEnv(settings.Environment); err != nil {
		return "", hostel.WrapError(hostel.ErrPreparationFailed, "session environment", err)
	}
	if len(settings.Environment) > 0 {
		var control strings.Builder
		for k, v := range settings.Environment {
			fmt.Fprintf(&control, "export %s=%s\n", k, shellx.Quote(v))
		}
		result, err := s.runLocked(ctx, control.String(), nil)
		if err != nil {
			return "", err
		}
		if result.ExitCode != 0 {
			return "", hostel.WrapError(hostel.ErrPreparationFailed, "session environment", fmt.Errorf("environment update rejected"))
		}
	}
	if settings.Directory == "" {
		return "", nil
	}
	expression, err := directoryExpression(settings.Directory)
	if err != nil {
		return "", hostel.WrapError(hostel.ErrPreparationFailed, "session directory", err)
	}
	var output strings.Builder
	result, err := s.runLocked(ctx, `(printf '%s\n' "$PWD"; printf '%s\n' `+expression+`)`, func(line string) { output.WriteString(line) })
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", hostel.WrapError(hostel.ErrPreparationFailed, "session directory", fmt.Errorf("cannot expand directory"))
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 || lines[1] == "" {
		return "", hostel.WrapError(hostel.ErrPreparationFailed, "session directory", fmt.Errorf("invalid expanded directory"))
	}
	directory := lines[1]
	if !path.IsAbs(directory) {
		directory = path.Join(lines[0], directory)
	}
	resolved, err := s.view.ResolveDirectory(directory)
	if err != nil {
		return "", hostel.WrapError(hostel.ErrPreparationFailed, "session directory", err)
	}
	return resolved, nil
}
func directoryExpression(value string) (string, error) {
	if strings.ContainsAny(value, "\n\r\x00") {
		return "", fmt.Errorf("directory contains a control character")
	}
	var out strings.Builder
	if value == "~" || strings.HasPrefix(value, "~/") {
		out.WriteString(`"${HOME:?}"`)
		value = value[1:]
	}
	for len(value) > 0 {
		i := strings.IndexByte(value, '$')
		if i < 0 {
			out.WriteString(shellx.Quote(value))
			break
		}
		out.WriteString(shellx.Quote(value[:i]))
		value = value[i+1:]
		name := ""
		if strings.HasPrefix(value, "{") {
			end := strings.IndexByte(value, '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated directory variable")
			}
			name = value[1:end]
			value = value[end+1:]
		} else {
			n := 0
			for n < len(value) && ((value[n] >= 'a' && value[n] <= 'z') || (value[n] >= 'A' && value[n] <= 'Z') || (value[n] >= '0' && value[n] <= '9') || value[n] == '_') {
				n++
			}
			name = value[:n]
			value = value[n:]
		}
		if !sessionVariable.MatchString(name) {
			return "", fmt.Errorf("directory supports only $NAME and ${NAME} variables")
		}
		fmt.Fprintf(&out, `"${%s:?}"`, name)
	}
	return out.String(), nil
}

var sessionVariable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
