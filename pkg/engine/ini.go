package engine

// Copyright (c) 2018 Bhojpur Consulting Private Limited, India. All rights reserved.

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

import (
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
)

type IniSection struct {
	ValueMap   map[string][2]string
	SectionMap map[string]*IniSection
	Values     [][3]string // original order in the ini file
	Sections   []*IniSection
	Parent     *IniSection
	Depth      int
	Name       string
}

func newSection(name string, depth int, parent *IniSection) *IniSection {
	s := &IniSection{
		Name:       name,
		Depth:      depth,
		Parent:     parent,
		ValueMap:   make(map[string][2]string),
		SectionMap: make(map[string]*IniSection),
	}
	if parent != nil {
		parent.SectionMap[name] = s
		parent.Sections = append(parent.Sections, s)
	}
	return s
}

type IniErrSyntax struct {
	Line int
	Text string
}

func (e IniErrSyntax) Error() string {
	return fmt.Sprintf("invalid INI syntax on line %d: %s", e.Line, e.Text)
}

func ParseIniFile(fn string) (*IniSection, error) {
	b, err := ioutil.ReadFile(fn)
	if err != nil {
		return nil, err
	}
	return ParseIni(string(b))
}

func ParseIni(str string) (*IniSection, error) {
	lines := strings.Split(str, "\n")
	s := newSection("", 0, nil)
	top := s
	for ln, line := range lines {
		ln = ln + 1
		line = strings.Trim(line, " \t\n\r")
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			depth := 0
			for i := 0; i < len(line); i++ {
				if line[i] != '[' {
					break
				}
				depth = i + 1
			}
			sname := line[depth : len(line)-depth]
			if depth > s.Depth+1 {
				return nil, IniErrSyntax{Line: ln, Text: "section with wrong depth"}
			}
			n := s.Depth - depth + 1
			for i := 0; i < n; i++ {
				s = s.Parent
			}
			parent := s
			s = parent.SectionMap[sname]
			if s != nil {
				return nil, IniErrSyntax{Line: ln, Text: "duplicate section name on the same level"}
			}
			s = newSection(sname, depth, parent)
		} else {
			n := strings.Index(line, "=")
			if n <= 0 {
				continue
			}
			a := strings.Trim(line[:n], " \t\n\r")
			b := strings.Trim(line[n+1:], " \t\n\r")
			if _, ok := s.ValueMap[a]; ok {
				return nil, IniErrSyntax{Line: ln, Text: "duplicate section name on the same level"}
			}
			lns := strconv.Itoa(ln)
			s.ValueMap[a] = [2]string{b, lns}
			s.Values = append(s.Values, [3]string{a, b, lns})
		}
	}
	return top, nil
}
