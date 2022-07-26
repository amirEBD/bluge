//  Copyright (c) 2020 The Bluge Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 		http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bluge

import (
	"context"
	"log"

	"github.com/blugelabs/bluge/search"
	"github.com/blugelabs/bluge/search/collector"
	"golang.org/x/sync/errgroup"
)

type MultiSearcherList struct {
	searchers []search.Searcher
	docChan   chan *search.DocumentMatch
}

func NewMultiSearcherList(searchers []search.Searcher, cc *collector.CollectorConfig) *MultiSearcherList {
	m := &MultiSearcherList{
		searchers: searchers,
		docChan:   make(chan *search.DocumentMatch, len(searchers)*2),
	}
	go m.collectAllDocuments(cc)
	return m
}

// if one searcher fails, should stop all the rest and exit?
func (m *MultiSearcherList) collectAllDocuments(cc *collector.CollectorConfig) {
	errs := errgroup.Group{}
	errs.SetLimit(1000)

	size := (cc.BackingSize + m.DocumentMatchPoolSize()) / len(m.searchers)
	size += 100
	for i := range m.searchers {
		s := m.searchers[i]
		errs.Go(func() error {
			ctx := search.NewSearchContext(size, len(cc.Sort))

			dm, err := s.Next(ctx)

			for err == nil && dm != nil {

				if len(cc.NeededFields) > 0 {
					err = dm.LoadDocumentValues(ctx, cc.NeededFields)
					if err != nil {
						return err
					}
				}

				// compute this hits sort value
				//cc.Sort.Compute(dm)

				dm.Context = ctx
				m.docChan <- dm
				dm, err = s.Next(ctx)
			}

			return err
		})
	}

	err := errs.Wait()
	if err != nil {
		log.Printf("multisearcher failed: %s", err.Error())
	}

	close(m.docChan)
}

func (m *MultiSearcherList) Next(_ *search.Context) (*search.DocumentMatch, error) {
	return <-m.docChan, nil
}

func (m *MultiSearcherList) DocumentMatchPoolSize() int {
	// we search sequentially, so just use largest
	var rv int
	for _, searcher := range m.searchers {
		ps := searcher.DocumentMatchPoolSize()
		if ps > rv {
			rv = ps
		}
	}
	return rv
}
func (m *MultiSearcherList) Close() (err error) {
	for _, searcher := range m.searchers {
		cerr := searcher.Close()
		if err == nil {
			err = cerr
		}
	}
	return err
}
func MultiSearch(ctx context.Context, req SearchRequest, readers ...*Reader) (search.DocumentMatchIterator, error) {
	searchers := make([]search.Searcher, 0, len(readers))
	for _, reader := range readers {
		searcher, err := req.Searcher(reader.reader, reader.config)
		if err != nil {
			return nil, err
		}
		searchers = append(searchers, searcher)
	}

	aggs := req.Aggregations()
	msl := NewMultiSearcherList(searchers, req.CollectorConfig(aggs))

	collector := req.Collector(true)
	dmItr, err := collector.Collect(ctx, aggs, msl)
	if err != nil {
		return nil, err
	}

	return dmItr, nil
}
