---
name: manage-model-diversity
description: Manage model diversity across OpenCode agents by optimizing model assignments and enabling multi-model review workflows
---

# Model Diversity Manager

This skill manages model diversity across OpenCode agents by optimizing model assignments and enabling multi-model review workflows.

## Usage

```bash
# Interactive setup - will analyze current config and suggest diversity improvements
/manage-model-diversity

# Direct invocation with specific mode
/manage-model-diversity mode=optimize
/manage-model-diversity mode=add-kimi
/manage-model-diversity mode=dual-reviewer
```

## Parameters

- `mode`: Operation mode - `analyze`, `optimize`, `add-kimi`, `dual-reviewer`
- `force`: Override safety checks (default: false)
- `dry-run`: Show changes without applying (default: false)

## What It Does

1. **Analyze**: Reads current agent configuration and identifies diversity gaps
2. **Optimize**: Reassigns models to maximize diversity across agents
3. **Add-Kimi**: Adds Kimi K2.5 agent with optimal configuration
4. **Dual-Reviewer**: Creates second reviewer agent for cross-validation

## Model Diversity Strategy

Based on available OpenCode models:
- **MiniMax-M2.1**: Heavy lifting, coding, testing (6 agents)
- **GLM-4.7**: Planning, exploration, architecture (3 agents) 
- **Kimi-K2.5**: Enhanced reasoning, quick tasks, review (new agent)

## Optimal Assignment

| Agent | Current Model | Proposed Model | Reason |
|-------|---------------|----------------|--------|
| architect | GLM-4.7 | GLM-4.7 | Planning stays with GLM |
| coder | MiniMax-M2.1 | MiniMax-M2.1 | Heavy lifting stays |
| reviewer | GLM-4.7 | Kimi-K2.5 | Enhanced reasoning |
| debugger | MiniMax-M2.1 | MiniMax-M2.1 | Debugging stays |
| quick | MiniMax-M2.1 | Kimi-K2.5 | Fast, enhanced reasoning |
| orchestrate | MiniMax-M2.1 | MiniMax-M2.1 | Coordination stays |
| explorer | GLM-4.7 | GLM-4.7 | Exploration stays |
| tester | MiniMax-M2.1 | MiniMax-M2.1 | Testing stays |
| committer | MiniMax-M2.1 | MiniMax-M2.1 | Git operations stay |
| kimi-reviewer | Kimi-K2.5 | Kimi-K2.5 | New dual-review agent |

## Dual Reviewer Setup

Creates two reviewer agents:
- `reviewer`: GLM-4.7 for traditional review
- `kimi-reviewer`: Kimi-K2.5 for enhanced reasoning review

## Benefits

1. **Enhanced Reasoning**: Kimi K2.5 provides superior reasoning for review tasks
2. **Cross-Validation**: Dual reviewers catch different types of issues
3. **Model Diversity**: Spreads load across different model architectures
4. **Performance**: Uses free models effectively

## Implementation

1. Updates `config/opencode.json` with new agent configurations
2. Preserves existing permissions and settings
3. Maintains backward compatibility
4. Creates clear documentation for the new setup

## Safety Checks

- Verifies model identifiers are valid
- Preserves existing agent descriptions
- Maintains permission structures
- Creates backup before applying changes

## Post-Setup

After running this skill:
- You'll have 10 agents instead of 9
- Kimi K2.5 will be available for quick tasks and enhanced review
- Dual reviewer system will be active
- Model diversity will be optimized

## Usage Examples

```bash
# Full optimization with backup
/manage-model-diversity mode=optimize force=true

# Add Kimi K2.5 only
/manage-model-diversity mode=add-kimi

# Show what would change without applying
/manage-model-diversity mode=analyze dry-run=true
```

## Model Selection Rationale

- **Kimi K2.5**: Superior reasoning, free tier available, excellent for review
- **GLM-4.7**: Strong planning capabilities, already configured
- **MiniMax-M2.1**: Reliable for coding tasks, already configured

## Performance Considerations

- Kimi K2.5 has higher output costs but excellent reasoning
- Dual reviewer provides better quality at modest cost increase
- Model diversity prevents single-point-of-failure

## Troubleshooting

If issues occur:
1. Check that `kimi-k2.5` is available in your OpenCode instance
2. Verify model identifiers match exactly
3. Check that agent files exist in `agents/` directory
4. Restore from backup if needed

## Requirements

- OpenCode with Zen provider access
- Available free models: kimi-k2.5, glm-4.7, minimax-m2.1
- Agent files in `agents/` directory
- Write access to `config/opencode.json`
