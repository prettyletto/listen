// Package ui contains the small, dependency-free terminal output surface.
package ui

import (
	"errors"
	"fmt"
	"io"

	"github.com/prettyletto/listen/internal/apperr"
)

type Output struct {
	Writer io.Writer
}

func New(writer io.Writer) Output { return Output{Writer: writer} }

func (o Output) Info(message string) error    { return o.write("", message) }
func (o Output) Success(message string) error { return o.write("✓ ", message) }
func (o Output) Warning(message string) error { return o.write("Warning: ", message) }

func (o Output) Error(err error) error {
	if err == nil {
		return nil
	}
	var userErr *apperr.Error
	if errors.As(err, &userErr) {
		if writeErr := o.write("listen: ", userErr.Message); writeErr != nil {
			return writeErr
		}
		if userErr.Hint != "" {
			return o.write("Hint: ", userErr.Hint)
		}
		return nil
	}
	return o.write("listen: ", err.Error())
}

func (o Output) write(prefix, message string) error {
	if o.Writer == nil {
		return errors.New("terminal output writer is nil")
	}
	_, err := fmt.Fprintf(o.Writer, "%s%s\n", prefix, message)
	return err
}
