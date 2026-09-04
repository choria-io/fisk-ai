import { useEffect, useState } from "react"

// AgentCard is what GET /api/agent answers, which is the worker's own description of
// itself rather than anything this page knows.
interface AgentCard {
  name: string
  version?: string
  description?: string
  model?: string
  protocols?: string[]
  tools?: { name: string; description?: string }[]
  telemetry?: boolean
}

// AgentRail shows what is answering. A browser has no other way to see the model, the
// version or the tool list, and those are the first things to check when a run does
// something surprising.
export const AgentRail = ({ endpoint }: { endpoint: string }) => {
  const [card, setCard] = useState<AgentCard | null>(null)
  const [failed, setFailed] = useState<string | null>(null)

  useEffect(() => {
    fetch(endpoint)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`${r.status}`))))
      .then(setCard)
      .catch((e: Error) => setFailed(e.message))
  }, [endpoint])

  return (
    <aside className="flex w-72 shrink-0 flex-col gap-7 border-border border-r bg-card px-6 py-7">
      <div>
        <h1 className="font-mono font-semibold text-[15px] tracking-tight">
          {card?.name ?? (failed ? "unreachable" : "connecting")}
        </h1>
        {card?.description && (
          <p className="mt-2 text-[13px] text-muted-foreground leading-snug">
            {card.description}
          </p>
        )}
        {failed && (
          <p className="mt-2 text-[13px] text-muted-foreground leading-snug">
            The bridge could not reach the agent. Check that a worker is serving and that
            the bridge points at it.
          </p>
        )}
      </div>

      {card && (
        <dl className="flex flex-col gap-2.5 text-[13px]">
          <Fact label="Model" value={card.model} />
          <Fact label="Version" value={card.version} />
          <Fact label="Protocol" value={card.protocols?.[0]} />
          <Fact label="Traces" value={card.telemetry ? "exported" : undefined} />
        </dl>
      )}

      {card?.tools && card.tools.length > 0 && (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <h2 className="text-[13px] text-muted-foreground">
            {card.tools.length} tools
          </h2>
          <ul className="mt-2.5 flex flex-col gap-2.5">
            {card.tools.map((tool) => (
              <li key={tool.name}>
                <span className="font-mono text-[13px]">{tool.name}</span>
                {tool.description && (
                  <p className="mt-0.5 text-[12px] text-muted-foreground leading-snug">
                    {tool.description}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </aside>
  )
}

// Fact is one line of the card. A value the agent did not publish is left out rather than
// shown empty, since an absent model means the agent said nothing and not that it has
// none.
const Fact = ({ label, value }: { label: string; value?: string }) => {
  if (!value) {
    return null
  }

  return (
    <div className="flex justify-between gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="truncate font-mono text-[12px]">{value}</dd>
    </div>
  )
}
