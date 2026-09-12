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

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/executor"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func TestCommandStdinFileRoundTrip(t *testing.T) {
	for _, background := range []bool{false, true} {
		mode := "foreground"
		if background {
			mode = "background"
		}
		for _, tc := range []struct{ name, input string }{
			{"empty", ""},
			{"literal", "中文\n'\"$HOME $(exit 9) `exit 8`\nno-final-newline"},
			{"large", strings.Repeat("工具结果正文\n", 65536)},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				s := newTestServer(t)
				payload, err := json.Marshal(map[string]any{
					"command": "umask 077; mkdir -p tool_results && cat > tool_results/result.tmp && mv tool_results/result.tmp tool_results/result",
					"cwd":     "/workspace", "stdin": tc.input, "background": background, "timeout": 3000,
				})
				if err != nil {
					t.Fatal(err)
				}
				rec := do(t, s, http.MethodPost, "/command", bytes.NewReader(payload), map[string]string{"Content-Type": "application/json"})
				if rec.Code != http.StatusOK {
					t.Fatalf("command: %d %s", rec.Code, rec.Body.String())
				}
				var executionID string
				for _, event := range parseSSE(t, rec.Body.String()) {
					if event.Type == EventExecutionStart {
						executionID = event.ExecutionID
					}
				}
				execution, ok := s.mgr.Executions().Get(executionID)
				if !ok {
					t.Fatalf("execution %q not registered", executionID)
				}
				result := execution.Wait()
				if result.Cause != bed.CauseNatural || result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 {
					t.Fatalf("execution failed: %+v", result)
				}
				file := do(t, s, http.MethodGet, "/files/download?path=/workspace/tool_results/result", nil, nil)
				if file.Code != http.StatusOK || file.Body.String() != tc.input {
					t.Fatalf("saved result: status=%d bytes=%d, want %d exact bytes", file.Code, file.Body.Len(), len(tc.input))
				}
			})
		}
	}
}
