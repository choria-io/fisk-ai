+++
title = "Development Roadmap"
description = "planned direction for the Fisk AI platform"
toc = true
weight = 20
+++

Fisk AI aims to become a platform for building AI-powered applications in the operations space, closely integrated with
Choria.

In large environments where Choria manages hundreds of thousands of machines, teams need to build AI agents that operate
autonomously. These agents must cooperate with each other. A Redis team's Outage Triage Agent must be able to
communicate with a Monitoring team's Metrics Agent to assist with its duties.

Cooperation across a Choria network requires Identity, Authorization, and Auditing. The Choria Protocol already provides
these features. Fisk AI will build an Agent-to-Agent (A2A) system on top of the Choria Protocol.

The current focus is the day-to-day operator who automates tasks with LLMs on a local workstation.

## Roadmap

* ~A `fisk-ai` native RAG database that integrates with local LLMs to create embeddings. The initial scope is Markdown
  files, allowing local knowledge bases that agents can interact with. These databases can also be exposed to systems
  like Claude Code.~ Released in version 0.0.2 See [Knowledge]({{% relref "knowledge" %}})
* Choria Protocol A2A system
    * Identity
    * Auditing
    * Authorization
    * Tool sharing using Choria Services
* NATS Server and Choria Broker integration
    * Memory in Key-Value stores
    * Sessions in Streams
    * Agent configuration in Key-Value stores
* Work queue support for ingesting data and completing tasks