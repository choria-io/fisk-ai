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
  answered: boolean
  onAnswer: (answer: FiskAnswer, said: string) => void
}

// QuestionCard renders a yes or no, a pick one of several, or a line of text with a
// default, and sends the answer as a request of its own.
//
// An answered card stays on the page as the record of what was asked and stops taking
// input: the part it was drawn from is still in the history that every later request
// resends, so it is drawn again on every render after the answer.
export const QuestionCard = ({ question, answered, onAnswer }: QuestionCardProps) => {
  const [value, setValue] = useState(question.default ?? "")

  const answer = (a: Omit<FiskAnswer, "toolUseId" | "kind">, said: string) => {
    onAnswer({ toolUseId: question.toolUseId, kind: question.kind, ...a }, said)
  }

  return (
    <Alert className="mb-4 flex flex-col gap-3">
      <AlertDescription className="inline font-medium">{question.question}</AlertDescription>

      {question.kind === "confirm" && (
        <div className="flex justify-end gap-2">
          <Button
            className="h-8 px-3 text-sm"
            disabled={answered}
            onClick={() => answer({ confirmed: true }, "Yes")}
            type="button"
          >
            Yes
          </Button>
          <Button
            className="h-8 px-3 text-sm"
            disabled={answered}
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
              className="h-8 justify-start px-3 text-sm"
              disabled={answered}
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
            answer({ value }, value)
          }}
        >
          <Input
            disabled={answered}
            onChange={(e) => setValue(e.target.value)}
            placeholder={question.default}
            value={value}
          />
          <Button className="h-9 px-3 text-sm" disabled={answered} type="submit">
            Answer
          </Button>
        </form>
      )}
    </Alert>
  )
}
