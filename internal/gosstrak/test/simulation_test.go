// Copyright (c) 2018 Iori Mizutani
//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package main

import (
	//"bytes"
	//"encoding/gob"
	"fmt"
	"testing"

	"github.com/iomz/tagstrak/internal/gosstrak/filtering"
)

func BenchmarkSimulatedEngineCreation(b *testing.B) {
	for nSub := 1000; nSub <= 10000; nSub += 1000 {
		for _, mp := range []int{0, 25, 50, 75, 100} {
			for en, ec := range filtering.AvailableEngines {
				b.Run(fmt.Sprintf("%s-%v-%v", en, mp, nSub), func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						sub := filtering.LoadSubscriptionsFromCSVFile(fmt.Sprintf("data/simulation/dataset%v-%vpct/ecspec.csv", nSub, mp))
						engine := ec(sub)
						_ = engine
					}
				})
			}
		}
	}
}
