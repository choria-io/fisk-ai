import { useState } from "react"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// FiskQuestion is the data-question part the Go endpoint sends when a run stops on one of
// the three human-in-the-loop tools. The AI SDK has no shape for a question that is not a
// tool approval, so it travels as a data part and this is the component that draws it.
export interface FiskQuestion {
  questionId: string
  toolUseId: string
  kind: string
  question: string
  options?: string[]
  default?: string
}

// FiskAnswer is what goes back in the request body. It names the call rather than the
// question, because a resumed run mints a new question id and the worker correlates an
// answer on the tool call it belongs to.
export interface FiskAnswer {
  toolUseId: string
  kind: string
  confirmed?: boolean
  value?: string
}

export interface QuestionCardProps {
  question: FiskQuestion
  // answered is what was said, and undefined for a question still open.
  answered?: string
  onAnswer: (answer: FiskAnswer, said: string) => void
}

// QuestionCard renders a yes or no, a pick one of several, or a line of text with a
// default, and sends the answer as a request of its own.
//
// An answered card keeps the question and shows the answer beside it, because sending an
// answer adds no turn to the transcript and this is the only place it appears. It is
// drawn again on every render after that, since the part it comes from stays in the
// history that each later request resends.
export const QuestionCard = ({ question, answered, onAnswer }: QuestionCardProps) => {
  const [value, setValue] = useState(question.default ?? "")

  const answer = (a: Omit<FiskAnswer, "toolUseId" | "kind">, said: string) => {
    onAnswer({ toolUseId: question.toolUseId, kind: question.kind, ...a }, said)
  }

  if (answered !== undefined) {
    return (
      <Alert className="flex flex-col gap-1.5">
        <AlertDescription className="inline text-muted-foreground">
          {question.question}
        </AlertDescription>
        <p className="font-mono text-[13px] text-foreground">{answered}</p>
      </Alert>
    )
  }

  // Amber says a person is needed, the same signal the approval carries.
  return (
    <Alert className="flex flex-col gap-3 border-signal-rule bg-signal-quiet">
      <AlertDescription className="inline">{question.question}</AlertDescription>

      {question.kind === "confirm" && (
        <div className="flex justify-end gap-2">
          <Button
            className="h-8 bg-signal px-3 text-sm text-signal-foreground hover:bg-signal/90"
            onClick={() => answer({ confirmed: true }, "Yes")}
            type="button"
          >
            Yes
          </Button>
          <Button
            className="h-8 px-3 text-sm"
            onClick={() => answer({ confirmed: false }, "No")}
            type="button"
            variant="outline"
          >
            No
          </Button>
        </div>
      )}

      {question.kind === "select" && (
        <div className="flex flex-col gap-2">
          {(question.options ?? []).map((option) => (
            <Button
              className="h-8 justify-start px-3 font-mono text-sm"
              key={option}
              onClick={() => answer({ value: option }, option)}
              type="button"
              variant="outline"
            >
              {option}
            </Button>
          ))}
        </div>
      )}

      {question.kind === "input" && (
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()

            if (value.trim() !== "") {
              answer({ value }, value)
            }
          }}
        >
          <Input
            autoFocus
            className="font-mono"
            onChange={(e) => setValue(e.target.value)}
            placeholder={question.default}
            value={value}
          />
          <Button
            className="h-9 bg-signal px-3 text-sm text-signal-foreground hover:bg-signal/90"
            type="submit"
          >
            Answer
          </Button>
        </form>
      )}
    </Alert>
  )
}
