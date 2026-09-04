import { useMemo, useState } from "react"

import { useChat } from "@ai-sdk/react"
import { DefaultChatTransport } from "ai"

import {
  Confirmation,
  ConfirmationAction,
  ConfirmationActions,
  ConfirmationRequest,
  ConfirmationTitle,
} from "@/components/ai-elements/confirmation"
import {
  Conversation,
  ConversationContent,
  ConversationEmptyState,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation"
import { Message, MessageContent, MessageResponse } from "@/components/ai-elements/message"
import {
  PromptInput,
  PromptInputBody,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
} from "@/components/ai-elements/prompt-input"
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from "@/components/ai-elements/reasoning"
import {
  Tool,
  ToolContent,
  ToolHeader,
  ToolInput,
  ToolOutput,
} from "@/components/ai-elements/tool"
import { AgentRail } from "@/AgentRail"
import { QuestionCard, type FiskAnswer, type FiskQuestion } from "@/QuestionCard"

// endpoint is where fisk-web listens. It is a different origin from Vite's dev server, so
// the Go side answers a preflight before every one of these.
const endpoint = "http://localhost:8080/api/chat"

// cardEndpoint is the agent's own description of itself, asked once when the page loads.
const cardEndpoint = "http://localhost:8080/api/agent"

// chatID names the conversation. The Go side maps it to the a2a conversation token the
// worker minted, so it has to be the same across every request of a thread and a reload
// starts a new one.
const chatID = crypto.randomUUID()

const App = () => {
  // answered is what this page said to each question it has answered. A data-question part
  // stays in the history and is drawn again on every render, so without this the card
  // would go on taking input after the run had moved past it. The answer is kept rather
  // than a flag because the card is the only place it appears: sending it adds no turn.
  const [answered, setAnswered] = useState<Map<string, string>>(new Map())

  const { messages, sendMessage, addToolApprovalResponse, status } = useChat({
    id: chatID,
    transport: new DefaultChatTransport({ api: endpoint }),
  })

  // decide answers an approval and starts the turn that acts on it.
  //
  // sendAutomaticallyWhen with lastAssistantMessageIsCompleteWithApprovalResponses is the
  // AI SDK's own way to do the second half, and it did not fire here.
  // addToolApprovalResponse skips the automatic send outright while the response is still
  // streaming or submitted, and never comes back to it, and the helper additionally wants
  // every tool part in the message to have reached a terminal state. Neither condition is
  // visible from outside, so the run sat suspended until somebody typed.
  //
  // addToolApprovalResponse still records the decision, which is what takes the card off
  // the page and what a later render reads. The request carries the answer in its own
  // field rather than leaving the server to find it in the history, which is the same path
  // the three question tools already use.
  const decide = (toolCallId: string, approvalId: string, approved: boolean) => {
    addToolApprovalResponse({ id: approvalId, approved })
    sendMessage(undefined, {
      body: { fiskAnswer: { toolUseId: toolCallId, kind: "approve", confirmed: approved } },
    })
  }

  // A gated call is sent twice. The bridge invents a tool part on the turn that stops to
  // ask, because an approval mutates a part rather than making one, and the resumed turn
  // sends the call again once the runner traces it for real. The two land in different
  // assistant messages, so the client cannot merge them by tool call id.
  //
  // Drawing only the last part for each id makes the pair one card, which ends in what
  // the call returned.
  // The same rule applies to a question: a resumed run can carry its question part again,
  // and one question is one card wherever it arrives.
  const latest = useMemo(() => {
    const last = new Map<string, string>()

    for (const message of messages) {
      message.parts.forEach((part, i) => {
        if (part.type === "dynamic-tool") {
          last.set(part.toolCallId, `${message.id}-${i}`)
        }

        if (part.type === "data-question") {
          last.set((part.data as FiskQuestion).questionId, `${message.id}-${i}`)
        }
      })
    }

    return last
  }, [messages])

  // answer sends what the person said about one of the three question tools.
  //
  // It sends no message. sendMessage takes an optional one, so the request goes with the
  // answer in its body and nothing is added to the transcript: echoing the answer as a
  // user turn would say the same thing twice, once in the card and once below it.
  const answer = (question: FiskQuestion, a: FiskAnswer, said: string) => {
    setAnswered((seen) => new Map(seen).set(question.questionId, said))
    sendMessage(undefined, { body: { fiskAnswer: a } })
  }

  return (
    <div className="flex h-screen">
      <AgentRail endpoint={cardEndpoint} />

      <main className="flex min-w-0 flex-1 flex-col">
        <Conversation>
          <ConversationContent className="mx-auto w-full max-w-2xl px-6 py-8">
          {messages.length === 0 && (
            <ConversationEmptyState
              description="Whatever it runs on your behalf, it asks first. The approval lands here."
              title="Ask the agent to do something"
            />
          )}

          {messages.map((message) => (
            <Message from={message.role} key={message.id}>
              <MessageContent>
                {message.parts.map((part, i) => {
                  const key = `${message.id}-${i}`

                  if (part.type === "text") {
                    return <MessageResponse key={key}>{part.text}</MessageResponse>
                  }

                  if (part.type === "reasoning") {
                    return (
                      // defaultOpen false also stops it opening itself while it
                      // streams, so thinking stays a line you can pull on rather
                      // than something that pushes the answer down the page.
                      <Reasoning
                        defaultOpen={false}
                        isStreaming={part.state === "streaming"}
                        key={key}
                      >
                        <ReasoningTrigger />
                        <ReasoningContent>{part.text}</ReasoningContent>
                      </Reasoning>
                    )
                  }

                  // Every tool call arrives dynamic, because the page cannot know a remote
                  // agent's tool names when it is built.
                  if (part.type === "dynamic-tool") {
                    if (latest.get(part.toolCallId) !== key) {
                      return null
                    }

                    // The three question tools are drawn by their question card, which is
                    // the whole of their user interface. A card saying ask_human_input is
                    // running, above the question it is running to ask, says nothing.
                    if (part.toolName.startsWith("ask_human_")) {
                      return null
                    }

                    return (
                      <div className="flex flex-col gap-2" key={key}>
                        <Tool>
                          <ToolHeader
                            state={part.state}
                            toolName={part.toolName}
                            type="dynamic-tool"
                          />
                          <ToolContent>
                            <ToolInput input={part.input} />
                            <ToolOutput
                              errorText={part.errorText}
                              output={part.output}
                            />
                          </ToolContent>
                        </Tool>

                        {/* Only a call actually waiting on somebody draws this, and amber
                            is the page's only signal color so nothing else may wear it.
                            Answering it takes the card away rather than settling it: the
                            suspend and the resume underneath are machinery, and the tool
                            card's own result is what says what happened. */}
                        {part.state === "approval-requested" && (
                        <Confirmation
                          approval={part.approval}
                          className="border-signal-rule bg-signal-quiet"
                          state={part.state}
                        >
                          <ConfirmationTitle>
                            May the agent run{" "}
                            <span className="font-mono">{part.toolName}</span>?
                          </ConfirmationTitle>
                          <ConfirmationRequest>
                            <ConfirmationActions>
                              <ConfirmationAction
                                className="h-8 bg-signal px-3 text-sm text-signal-foreground hover:bg-signal/90"
                                onClick={() =>
                                  part.approval &&
                                  decide(part.toolCallId, part.approval.id, true)
                                }
                              >
                                Allow
                              </ConfirmationAction>
                              <ConfirmationAction
                                onClick={() =>
                                  part.approval &&
                                  decide(part.toolCallId, part.approval.id, false)
                                }
                                variant="outline"
                              >
                                Deny
                              </ConfirmationAction>
                            </ConfirmationActions>
                          </ConfirmationRequest>
                        </Confirmation>
                        )}
                      </div>
                    )
                  }

                  if (part.type === "data-question") {
                    const question = part.data as FiskQuestion

                    if (latest.get(question.questionId) !== key) {
                      return null
                    }

                    return (
                      <QuestionCard
                        answered={answered.get(question.questionId)}
                        key={key}
                        onAnswer={(a, said) => answer(question, a, said)}
                        question={question}
                      />
                    )
                  }

                  return null
                })}
              </MessageContent>
            </Message>
          ))}
          </ConversationContent>
          <ConversationScrollButton />
        </Conversation>

        <div className="border-border border-t bg-card px-6 py-4">
          <PromptInput
            className="mx-auto max-w-2xl"
            onSubmit={(message) => {
              if (message.text.trim() !== "") {
                sendMessage({ text: message.text })
              }
            }}
          >
            <PromptInputBody>
              <PromptInputTextarea placeholder="Ask the agent something" />
            </PromptInputBody>
            <PromptInputFooter>
              <PromptInputSubmit status={status} />
            </PromptInputFooter>
          </PromptInput>
        </div>
      </main>
    </div>
  )
}

export default App
