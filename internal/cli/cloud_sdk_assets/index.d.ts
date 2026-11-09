export interface HostlierResult { stdout: string; stderr: string }
export interface HostlierInvocation { cwd?: string; signal?: AbortSignal; env?: Record<string, string | undefined>; input?: string | Uint8Array }
export type HostlierRunner = (args: string[], options: HostlierInvocation) => Promise<HostlierResult>;
export interface Compute { cpu: string; memory: string }
export interface ApplicationStatus {
  name: string;
  url?: string;
  status?: string;
  spec: Record<string, unknown>;
  runtimeStatus?: { running?: boolean; readyReplicas?: number };
  ready: boolean;
}
export interface CloudOptions {
  bundlePath: string;
  stateDirectory: string;
  /** Owner-only JSON exported from an enrolled Hostlier service profile. */
  credentialFile: string;
  /** Only for localhost integration tests. Remote controllers require HTTPS. */
  allowInsecureLocalhost?: boolean;
  server?: string;
  /** Test transport injection; normal SDK use calls the controller API directly. */
  run?: HostlierRunner;
}
export interface CreateInstance {
  name: string;
  route?: string;
  routes?: Record<string, string>;
  env?: Record<string, string>;
  secrets?: Record<string, string>;
  sidecarSecrets?: Record<string, Record<string, string>>;
  /** Overrides only workflow.steps[id=database].config.database for PostgreSQL new-database mode. */
  databaseName?: string;
  compute?: Compute;
  replicas?: number;
  signal?: AbortSignal;
}
export interface HostlierCloud {
  readonly bundleDigest: string;
  setSecret(name: string, value: string | Uint8Array, options?: { signal?: AbortSignal }): Promise<void>;
  setInstanceSecret(app: string, name: string, value: string | Uint8Array, options?: { signal?: AbortSignal }): Promise<void>;
  deleteSecret(name: string, options?: { signal?: AbortSignal }): Promise<void>;
  statusSnapshot(name: string, options?: { signal?: AbortSignal }): Promise<ApplicationStatus>;
  recoverPending(name: string, expectedImageDigest: string, options?: { signal?: AbortSignal }): Promise<ApplicationStatus>;
  purge(name: string, options?: { signal?: AbortSignal }): Promise<HostlierResult>;
  recoverPendingPurge(name: string, options?: { signal?: AbortSignal }): Promise<HostlierResult | void>;
  confirmPurge(name: string, operationId: string, options?: { signal?: AbortSignal }): Promise<void>;
  create(input: CreateInstance): Promise<HostlierResult>;
  /** Creates once, then observes only if the requested complete project still matches. */
  ensure(input: CreateInstance): Promise<ApplicationStatus>;
  setEnvironment(name: string, changes: Record<string, string>, options?: { signal?: AbortSignal }): Promise<HostlierResult>;
  setCompute(name: string, compute: Compute, options?: { signal?: AbortSignal }): Promise<HostlierResult>;
  expandSidecarVolume(name: string, sidecar: string, volume: string, size: string, options?: { signal?: AbortSignal }): Promise<HostlierResult>;
  status(name: string, options?: { signal?: AbortSignal }): Promise<HostlierResult>;
}
export declare function createHostlierCloud(options: CloudOptions): Promise<HostlierCloud>;
