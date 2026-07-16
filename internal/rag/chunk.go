//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"strings"
)

const (
	// targetChunkBytes is the size a chunk is packed toward. Chunking is
	// structure-aware: blocks are packed under their heading section up to this size
	// before a new chunk starts.
	targetChunkBytes = 1200

	// maxChunkBytes is the size above which a single paragraph block is hard-split.
	// A fenced code block is kept intact even past this, since splitting code hurts
	// retrieval more than an oversized chunk.
	maxChunkBytes = 1500
)

// Chunk is one heading-delimited, size-packed unit of a document. Content folds
// the heading breadcrumb into the body (which the spike showed pulls the on-topic
// section to rank 1 for any model), and HeadingPath is the same breadcrumb stored
// as its own FTS5 column, since the section title is often the most search-relevant
// phrase in a chunk.
type Chunk struct {
	HeadingPath string
	Content     string
}

// heading is one entry on the breadcrumb stack: a markdown heading level and its
// text.
type heading struct {
	level int
	text  string
}

// ChunkDocument splits document text into heading-delimited, size-packed chunks.
// Markdown headings delimit sections and build the breadcrumb; fenced code blocks
// and blank-line-delimited blocks (which keeps tables intact) are packed whole;
// plain text with no headings falls back to paragraph packing under an empty
// breadcrumb. Empty input yields no chunks.
func ChunkDocument(content string) []Chunk {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	var chunks []Chunk
	var stack []heading
	packer := sectionPacker{}

	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		if isFence(trimmed) {
			block, next := collectFence(lines, i)
			packer.add(block, true)
			i = next
			continue
		}

		if lvl, text, ok := parseHeading(trimmed); ok {
			// A heading starts a new section: flush the previous one, then update the
			// breadcrumb. The heading line itself is not added to the body; the folded
			// breadcrumb carries it.
			packer.flush(&chunks)
			stack = pushHeading(stack, lvl, text)
			packer = sectionPacker{breadcrumb: crumb(stack)}
			i++
			continue
		}

		if trimmed == "" {
			i++
			continue
		}

		block, next := collectParagraph(lines, i)
		packer.add(block, false)
		i = next
	}
	packer.flush(&chunks)

	return chunks
}

// DocumentTitle returns the first heading in the document, used as documents.title.
// A document with no heading has no title.
func DocumentTitle(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if isFence(trimmed) {
			_, next := collectFence(lines, i)
			i = next
			continue
		}
		if _, text, ok := parseHeading(trimmed); ok {
			return text
		}
		i++
	}

	return ""
}

// sectionPacker accumulates blocks under one breadcrumb and emits chunks packed
// toward the target size. buf is the pending (not yet emitted) chunk body; done
// holds chunks completed while packing this section but not yet flushed to output.
type sectionPacker struct {
	breadcrumb string
	buf        []string
	size       int
	done       []Chunk
}

// add appends a block to the current section, completing a chunk first when the
// block would push the pending chunk past the target. A paragraph block larger
// than maxChunkBytes is hard-split; an indivisible block (a code fence) is kept
// whole even when oversized.
func (p *sectionPacker) add(block string, indivisible bool) {
	block = strings.TrimRight(block, "\n")
	if strings.TrimSpace(block) == "" {
		return
	}

	if !indivisible && len(block) > maxChunkBytes {
		for start := 0; start < len(block); start += targetChunkBytes {
			end := min(start+targetChunkBytes, len(block))
			p.appendBlock(block[start:end])
		}
		return
	}

	p.appendBlock(block)
}

// appendBlock adds one block to the pending buffer, completing the current chunk
// first when the block would overflow the target size.
func (p *sectionPacker) appendBlock(block string) {
	if len(p.buf) > 0 && p.size+len(block)+2 > targetChunkBytes {
		p.done = append(p.done, p.take())
	}
	p.buf = append(p.buf, block)
	p.size += len(block) + 2
}

// take renders and clears the pending buffer as one chunk body.
func (p *sectionPacker) take() Chunk {
	body := strings.Join(p.buf, "\n\n")
	p.buf = nil
	p.size = 0

	return Chunk{HeadingPath: p.breadcrumb, Content: foldHeading(p.breadcrumb, body)}
}

// flush emits every chunk completed while packing plus the final pending buffer
// into out.
func (p *sectionPacker) flush(out *[]Chunk) {
	*out = append(*out, p.done...)
	p.done = nil
	if len(p.buf) > 0 {
		*out = append(*out, p.take())
	}
}

// foldHeading prefixes the body with its breadcrumb so the section title travels
// with the chunk text into the embedding and the lexical index.
func foldHeading(breadcrumb, body string) string {
	if breadcrumb == "" {
		return body
	}

	return breadcrumb + "\n\n" + body
}

// isFence reports whether a trimmed line opens or closes a fenced code block.
func isFence(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// collectFence returns the whole fenced code block starting at lines[i] (through
// its closing fence, or end of input) and the index just past it. A fence is kept
// intact so code is never split across chunks.
func collectFence(lines []string, i int) (string, int) {
	open := strings.TrimSpace(lines[i])
	marker := "```"
	if strings.HasPrefix(open, "~~~") {
		marker = "~~~"
	}

	var b strings.Builder
	b.WriteString(lines[i])
	j := i + 1
	for j < len(lines) {
		b.WriteByte('\n')
		b.WriteString(lines[j])
		if strings.HasPrefix(strings.TrimSpace(lines[j]), marker) {
			j++
			break
		}
		j++
	}

	return b.String(), j
}

// collectParagraph returns the contiguous run of non-blank lines starting at
// lines[i] (which keeps a table's rows together) and the index just past it.
func collectParagraph(lines []string, i int) (string, int) {
	var b strings.Builder
	j := i
	for j < len(lines) {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || isFence(trimmed) {
			break
		}
		if _, _, ok := parseHeading(trimmed); ok {
			break
		}
		if j > i {
			b.WriteByte('\n')
		}
		b.WriteString(lines[j])
		j++
	}

	return b.String(), j
}

// parseHeading parses an ATX markdown heading ("## Title"), returning its level
// (number of leading #), its text, and whether the line is a heading.
func parseHeading(trimmed string) (int, string, bool) {
	if !strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}

	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 {
		return 0, "", false
	}

	text := strings.TrimSpace(trimmed[level:])
	if text == "" {
		return 0, "", false
	}

	return level, text, true
}

// pushHeading updates the breadcrumb stack for a new heading: it drops any entries
// at the same or deeper level, then pushes the new one, so the stack always holds
// the ancestor chain.
func pushHeading(stack []heading, level int, text string) []heading {
	for len(stack) > 0 && stack[len(stack)-1].level >= level {
		stack = stack[:len(stack)-1]
	}

	return append(stack, heading{level: level, text: text})
}

// crumb renders the breadcrumb stack as "A > B > C".
func crumb(stack []heading) string {
	parts := make([]string, len(stack))
	for i, h := range stack {
		parts[i] = h.text
	}

	return strings.Join(parts, " > ")
}
