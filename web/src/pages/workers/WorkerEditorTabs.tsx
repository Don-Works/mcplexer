// WorkerEditorTabs — tabbed layout for the worker create/edit form.
// Replaces the sprawling card stack with compact tabs. Each tab panel
// renders one or more of the existing card components so no form logic
// is duplicated — just the wrapping chrome changes.

import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs'
import type { AuthScope, Workspace } from '@/api/types'
import type { SkillRegistryEntry } from '@/api/client'
import type { EditorState } from './worker-editor-state'
import {
  BasicsCard,
  ModelCard,
  PromptCard,
  ScheduleCard,
  SkillCard,
  ToolsCard,
} from './WorkerEditorCards'
import { ExecutionCard, OutputCard } from './WorkerEditorExecCard'
import { LimitsCard } from './WorkerEditorLimitsCard'

type Setter = <K extends keyof EditorState>(key: K, value: EditorState[K]) => void

interface Props {
  state: EditorState
  set: Setter
  workspaces: Workspace[] | null
  authScopes: AuthScope[] | null
  tools: Parameters<typeof ToolsCard>[0]['tools']
  skills: SkillRegistryEntry[] | null
  onSecretCreated?: (scopeID: string) => void
}

export function WorkerEditorTabs({
  state,
  set,
  workspaces,
  authScopes,
  tools,
  skills,
  onSecretCreated,
}: Props) {
  return (
    <Tabs defaultValue="basics" className="w-full">
      <div className="sticky top-0 z-10 -mx-1 -mt-1 bg-background pb-1 pt-1">
        <TabsList className="w-full flex-nowrap overflow-x-auto" data-testid="worker-editor-tabs">
          <TabsTrigger value="basics" data-testid="worker-editor-tab-basics">Basics</TabsTrigger>
          <TabsTrigger value="model" data-testid="worker-editor-tab-model">Model</TabsTrigger>
          <TabsTrigger value="prompt" data-testid="worker-editor-tab-prompt">Prompt</TabsTrigger>
          <TabsTrigger value="schedule" data-testid="worker-editor-tab-schedule">Schedule</TabsTrigger>
          <TabsTrigger value="tools" data-testid="worker-editor-tab-tools">Tools</TabsTrigger>
          <TabsTrigger value="output" data-testid="worker-editor-tab-output">Output</TabsTrigger>
          <TabsTrigger value="execution" data-testid="worker-editor-tab-execution">Execution</TabsTrigger>
          <TabsTrigger value="limits" data-testid="worker-editor-tab-limits">Limits</TabsTrigger>
          <TabsTrigger value="skills" data-testid="worker-editor-tab-skills">Skills</TabsTrigger>
        </TabsList>
      </div>

      <TabsContent value="basics">
        <BasicsCard state={state} set={set} workspaces={workspaces} />
      </TabsContent>

      <TabsContent value="model">
        <ModelCard
          state={state}
          set={set}
          authScopes={authScopes}
          onSecretCreated={onSecretCreated}
        />
      </TabsContent>

      <TabsContent value="prompt">
        <PromptCard state={state} set={set} />
      </TabsContent>

      <TabsContent value="schedule">
        <ScheduleCard state={state} set={set} />
      </TabsContent>

      <TabsContent value="tools">
        <ToolsCard state={state} set={set} tools={tools} />
      </TabsContent>

      <TabsContent value="output">
        <OutputCard state={state} set={set} />
      </TabsContent>

      <TabsContent value="execution">
        <ExecutionCard state={state} set={set} />
      </TabsContent>

      <TabsContent value="limits">
        <LimitsCard state={state} set={set} />
      </TabsContent>

      <TabsContent value="skills">
        <SkillCard state={state} set={set} skills={skills} />
      </TabsContent>
    </Tabs>
  )
}
