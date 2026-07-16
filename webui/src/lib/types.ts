export type Scope = "project" | "global";
export type SkillStatus = "canonical" | "symlink" | "copy" | "broken_symlink" | "disabled";

export interface HealthIssue {
  type: string;
  severity: "error" | "warning" | string;
  message: string;
  path?: string;
}

export interface ObservedPath {
  path: string;
  scope: Scope;
  agent: string;
  status: SkillStatus;
  target_path?: string;
}

export interface Visibility {
  agent: string;
  display: string;
  visible: boolean;
  reason: string;
  path?: string;
  status?: SkillStatus;
}

export interface LockEntry {
  source?: string;
  sourceType?: string;
  sourceUrl?: string;
  ref?: string;
  skillPath?: string;
  pluginName?: string;
}

export interface Skill {
  name: string;
  description: string;
  scope: Scope;
  canonical_path?: string;
  skill_path?: string;
  observed_paths: ObservedPath[] | null;
  visibility?: Visibility[] | null;
  local_lock?: LockEntry;
  global_lock?: LockEntry;
  health_issues: HealthIssue[] | null;
  disabled: boolean;
}

export interface AgentState {
  name: string;
  display: string;
  supported: boolean;
  detected: boolean;
  universal: boolean;
  supports_global: boolean;
  project_dir: string;
  global_dir?: string;
  project_dir_exists: boolean;
  global_dir_exists?: boolean;
}

export interface ScanResult {
  cwd: string;
  global_lock_path?: string;
  project_lock_path?: string;
  agents?: AgentState[];
  skills: Skill[];
  health_issues?: HealthIssue[];
  preflight?: {
    can_run_skills: boolean;
    tools: Record<string, { exists: boolean; path?: string }>;
  };
}

export interface ScanPayload {
  generation: number;
  read_only: boolean;
  result: ScanResult;
  sources: SourceGroup[];
}

export interface SourceGroup {
  id: string;
  label: string;
  skills: SkillRef[];
  discoverable: boolean;
}

export interface SkillRef {
  scope: Scope;
  name: string;
}

export interface PreviewRequest {
  action: string;
  skills?: SkillRef[];
  agent?: string;
  candidate_ids?: string[];
  global?: boolean;
}

export interface Preview {
  hash: string;
  generation: number;
  id: string;
  title: string;
  command: string;
  description: string;
  mutates: boolean;
  requires_confirm: boolean;
  dangerous: boolean;
  confirm_value?: string;
}

export interface RegistrySkill {
  id: string;
  display_name: string;
  slug: string;
  source: string;
  installs: number;
  invalid?: boolean;
  reason?: string;
  candidate_id?: string;
  installed: boolean;
}

export interface DiscoveredSkill {
  name: string;
  description: string;
  skill_path: string;
  candidate_id: string;
  installed: boolean;
}

export interface UpdatePlan {
  current: string;
  latest: string;
  status: "already_latest" | "available" | "unknown";
  command_preview: string;
  reason: string;
  release_url: string;
}
