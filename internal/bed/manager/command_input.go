// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package manager

import "os"

// commandInput owns a finite stdin stream. Both Executor backends receive the
// same pipe descriptor; supervisor transfers it without copying caller content
// into argv, environment, or its control protocol.
type commandInput struct {
	reader *os.File
	writer *os.File
	done   chan struct{}
}

func newCommandInput(content string) (*commandInput, error) {
	if content == "" {
		return nil, nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	input := &commandInput{reader: reader, writer: writer, done: make(chan struct{})}
	// Feeding asynchronously lets inputs larger than a pipe buffer start, and
	// lets the execution drain stdout while the child is still reading stdin.
	go func() {
		defer close(input.done)
		defer writer.Close()
		// A command may consume only a prefix or exit without reading. Its
		// process outcome remains authoritative when the reader closes early.
		_, _ = writer.WriteString(content)
	}()
	return input, nil
}

func (input *commandInput) file() *os.File {
	if input == nil {
		return nil
	}
	return input.reader
}

func (input *commandInput) closeReader() {
	if input != nil {
		_ = input.reader.Close()
	}
}

func (input *commandInput) close() {
	if input == nil {
		return
	}
	input.closeReader()
	// Close also interrupts a blocked writer if descendants retain stdin
	// after the command exits. Join before publishing execution completion.
	_ = input.writer.Close()
	<-input.done
}
