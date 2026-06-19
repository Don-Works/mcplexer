// distinctReposFrom returns sorted unique repo names from a message list.
export function distinctReposFrom<T extends { repo?: string }>(items: T[]): string[] {
  const set = new Set<string>()
  for (const m of items) {
    if (m.repo) set.add(m.repo)
  }
  return Array.from(set).sort()
}

// distinctBranchesFrom returns sorted unique branch names. Pass an
// optional repoFilter to scope to one repo's branches.
export function distinctBranchesFrom<
  T extends { repo?: string; branch?: string },
>(items: T[], repoFilter?: string): string[] {
  const set = new Set<string>()
  for (const m of items) {
    if (!m.branch) continue
    if (repoFilter && m.repo !== repoFilter) continue
    set.add(m.branch)
  }
  return Array.from(set).sort()
}

// reposOverlappingWorkspaces extracts the set of repo identifiers that
// match any currently-open workspace's `root_path`. The match is naive
// (suffix on root_path) — agents normally send the canonical repo so the
// overlap is exact.
export function reposOverlappingWorkspaces(
  meshRepos: string[],
  workspaces: { root_path: string }[],
): string[] {
  if (workspaces.length === 0) return []
  const overlap = new Set<string>()
  for (const r of meshRepos) {
    const tail = r.split('/').slice(-2).join('/')
    if (workspaces.some((w) => w.root_path.endsWith(tail))) {
      overlap.add(r)
    }
  }
  return Array.from(overlap).sort()
}
