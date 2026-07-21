import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const ANY = '__any__'

export interface MeshRepoFilterProps {
  repos: string[]
  branches: string[]
  selectedRepo: string
  selectedBranch: string
  onChange: (next: { repo: string; branch: string }) => void
}

// MeshRepoFilter is the repo + branch dropdown pair shown above the
// recent-messages panel. Empty string means "any". The values come from
// distinct repo/branch pairs we've already received, so the dropdown is
// always populated with concrete options the user has actually seen.
export function MeshRepoFilter(props: MeshRepoFilterProps) {
  const { repos, branches, selectedRepo, selectedBranch, onChange } = props
  const branchesForRepo = selectedRepo
    ? branches.filter((b) => b !== '')
    : branches.filter((b) => b !== '')

  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs text-muted-foreground">Filter:</span>
      <Select
        value={selectedRepo === '' ? ANY : selectedRepo}
        onValueChange={(v) =>
          onChange({ repo: v === ANY ? '' : v, branch: selectedBranch })
        }
      >
        <SelectTrigger className="h-8 w-[260px]" data-testid="mesh-repo-select">
          <SelectValue placeholder="Any repo" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>Any repo</SelectItem>
          {repos.map((r) => (
            <SelectItem key={r} value={r}>
              {r}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <Select
        value={selectedBranch === '' ? ANY : selectedBranch}
        onValueChange={(v) =>
          onChange({ repo: selectedRepo, branch: v === ANY ? '' : v })
        }
      >
        <SelectTrigger className="h-8 w-[200px]" data-testid="mesh-branch-select">
          <SelectValue placeholder="Any branch" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>Any branch</SelectItem>
          {branchesForRepo.map((b) => (
            <SelectItem key={b} value={b}>
              {b}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

