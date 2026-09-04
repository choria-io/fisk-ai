import { useState } from "react"

import { useChat } from "@ai-sdk/react"
import {
  DefaultChatTransport,
  lastAssistantMessageIsCompleteWithApprovalResponses,
} from "ai"

import {
  Confirmation,
  ConfirmationAccepted,
  ConfirmationAction,
  ConfirmationActions,
  ConfirmationRejected,
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
import { QuestionCard, type FiskAnswer, type FiskQuestion } from "@/QuestionCard"

// endpoint is where fisk-web listens. It is a different origin from Vite's dev server, so
// the Go side answers a preflight before every one of these.
const endpoint = "http://localhost:8080/api/chat"

// chatID names the conversation. The Go side maps it to the a2a conversation token the
// worker minted, so it has to be the same across every request of a thread and a reload
// starts a new one.
const chatID = crypto.randomUUID()

const App = () => {
  // answered records the questions this page has already sent an answer for. A
  // data-question part stays in the history and is drawn again on every render, so
  // without this the card would go on taking input after the run had moved past it.
  const [answered, setAnswered] = useState<Set<string>>(new Set())

  const { messages, sendMessage, addToolApprovalResponse, status } = useChat({
    id: chatID,
    transport: new DefaultChatTransport({ api: endpoint }),
    // An approval ends the turn, and this is what starts the next one the moment the
    // person has answered rather than making them type something to continue.
    sendAutomaticallyWhen: lastAssistantMessageIsCompleteWithApprovalResponses,
  })

  // answer sends what the person said about one of the three question tools. The text is
  // the transcript of their answer and the body is what the run reads: the endpoint takes
  // the answer and never looks at the prompt when both are present.
  const answer = (question: FiskQuestion, a: FiskAnswer, said: string) => {
    setAnswered((seen) => new Set(seen).add(question.questionId))
    sendMessage({ text: said }, { body: { fiskAnswer: a } })
  }

  return (
    <main className="mx-auto flex h-screen max-w-3xl flex-col p-4">
      <Conversation>
        <ConversationContent>
          {messages.length === 0 && (
            <ConversationEmptyState
              description="This page talks to a Fisk agent reached over NATS."
              title="Ask something"
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
                      <Reasoning isStreaming={part.state === "streaming"} key={key}>
                        <ReasoningTrigger />
                        <ReasoningContent>{part.text}</ReasoningContent>
                      </Reasoning>
                    )
                  }

                  // Every tool call arrives dynamic, because the page cannot know a remote
                  // agent's tool names when it is built.
                  if (part.type === "dynamic-tool") {
                    return (
                      <div key={key}>
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

                        <Confirmation approval={part.approval} state={part.state}>
                          <ConfirmationTitle>
                            Run {part.toolName}?
                          </ConfirmationTitle>
                          <ConfirmationRequest>
                            <ConfirmationActions>
                              <ConfirmationAction
                                onClick={() =>
                                  part.approval &&
                                  addToolApprovalResponse({
                                    id: part.approval.id,
                                    approved: true,
                                  })
                                }
                              >
                                Allow
                              </ConfirmationAction>
                              <ConfirmationAction
                                onClick={() =>
                                  part.approval &&
                                  addToolApprovalResponse({
                                    id: part.approval.id,
                                    approved: false,
                                  })
                                }
                                variant="outline"
                              >
                                Deny
                              </ConfirmationAction>
                            </ConfirmationActions>
                          </ConfirmationRequest>
                          <ConfirmationAccepted>
                            <ConfirmationTitle>You allowed it.</ConfirmationTitle>
                          </ConfirmationAccepted>
                          <ConfirmationRejected>
                            <ConfirmationTitle>You denied it.</ConfirmationTitle>
                          </ConfirmationRejected>
                        </Confirmation>
                      </div>
                    )
                  }

                  if (part.type === "data-question") {
                    const question = part.data as FiskQuestion

                    return (
                      <QuestionCard
                        answered={answered.has(question.questionId)}
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

      <PromptInput
        className="mt-4"
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
    </main>
  )
}

export default App
