// ©Hayabusa Cloud Co., Ltd. 2026. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package takt_test

import (
	"testing"

	"code.hybscloud.com/kont"
	"code.hybscloud.com/takt"
)

func TestLoopClearsBorrowedResultsOnReturn(t *testing.T) {
	for _, boundary := range []string{"idle-run", "fatal-poll", "fatal-run"} {
		t.Run(boundary, func(t *testing.T) {
			b := &boundedBackend{}
			if boundary != "idle-run" {
				b.moreAt = 2
			}
			l := takt.NewLoop[*boundedBackend, int](b, 2, takt.WithMaxCompletions(2))
			defer l.Drain()
			for i := range 2 {
				if _, _, err := l.SubmitExpr(kont.ExprPerform(numberedOp{i + 1})); err != nil {
					t.Fatal(err)
				}
			}
			borrowed, err := l.Poll()
			if err != nil || len(borrowed) == 0 {
				t.Fatalf("first batch: %v, %v", borrowed, err)
			}
			if boundary == "fatal-poll" {
				_, err = l.Poll()
			} else {
				_, err = l.Run()
			}
			if err != l.Failed() {
				t.Fatal(err)
			}
			for _, value := range borrowed {
				if value != 0 {
					t.Fatal("expired result retained", borrowed)
				}
			}
		})
	}
}
