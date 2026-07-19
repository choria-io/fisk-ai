//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"time"
)

// stampRequest fills in the framing fields of a standalone request header. The
// message constructors set only the protocol id, so a request still needs an id,
// the correlation and conversation tags, a timestamp, and the sender before it is
// schema-valid. A direct tool or discovery RPC is not part of a larger task or
// session, so id, request and conversation are all the same fresh id, and
// sequence is unused (the transport reply inbox handles correlation), matching the
// transport notes for direct tool calls.
func stampRequest(h *Header, sender string, recipient string) {
	id := NewID()

	h.ID = id
	h.Request = id
	h.Conversation = id
	h.Sequence = 0
	h.Time = time.Now().UTC()
	h.Sender = Identity{Name: sender}
	if recipient != "" {
		h.Recipient = &Identity{Name: recipient}
	}
}

// stampReply fills in the framing fields of a reply header so it echoes the
// request it answers. The request and conversation tags are copied from the
// inbound request, the sender becomes this agent's identity, and the recipient
// becomes the original sender.
func stampReply(h *Header, req *Header, sender string) {
	h.ID = NewID()
	h.Request = req.Request
	h.Conversation = req.Conversation
	h.Sequence = 0
	h.Time = time.Now().UTC()
	h.Sender = Identity{Name: sender}
	if req.Sender.Name != "" {
		h.Recipient = &Identity{Name: req.Sender.Name}
	}
}
