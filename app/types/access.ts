export type AccessMode = 'unrestricted' | 'selected' | 'deny_all';

export interface AccessTarget {
    root?: string;
    directory?: string;
    recursive?: boolean;
    class?: string;
}

export interface AccessCeiling {
    mode: AccessMode;
    targets: AccessTarget[];
}

export interface AccessDirectoryChoice {
    root: string;
    directory: string;
    label: string;
    available: boolean;
}

export interface AccessRevision {
    id: number;
    version: number;
    base_revision: number;
    status: 'draft' | 'preparing' | 'ready' | 'active' | 'failed' | 'cancelled';
    processed: number;
    total: number;
    message: string;
    updated_at: number;
    watermark?: number;
    policy?: AccessPolicy;
}

export interface AccessGroup { id: string; name: string; members: string[]; range: AccessCeiling }
export interface AccessRule { id: string; target: AccessTarget; mode: 'selected' | 'deny_all'; users: string[]; groups: string[] }
export interface AccessPolicy {
    groups: AccessGroup[];
    principals: { id: string; range: AccessCeiling }[];
    rules: AccessRule[];
    classes: { id: string; name: string }[];
    released_origins?: string[];
    origin_rules?: AccessRule[];
}
