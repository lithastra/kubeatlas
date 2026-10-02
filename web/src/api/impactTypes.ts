// Display contract for the additive v1 impact response. Fields are observations,
// never a prediction of availability, safe deletion, or effective permissions.
export type ImpactRelation = 'dependents' | 'dependencies';

export interface ImpactIdentity {
  id: string;
  clusterId?: string;
  namespace: string;
  kind: string;
  name: string;
  uid?: string;
  resourceVersion?: string;
  referenceOnly: boolean;
}

export interface ImpactStep {
  from: string;
  to: string;
  type: string;
  traversalFrom: string;
  traversalTo: string;
  ownerUIDStatus?: string;
  bindingScope?: string;
  bindingNamespace?: string;
}

export interface ImpactMatch {
  resource: ImpactIdentity;
  depth: number;
  path: ImpactStep[];
}

export interface ImpactFacet {
  resources: ImpactMatch[];
  counts: { total: number; direct: number; indirect: number; lowerBound: boolean };
  truncated: boolean;
  truncationReasons: string[];
  modeledTraversalComplete: boolean;
  observationCoverage: string;
  notices: Array<{ reason: string; count: number }>;
}

export interface ImpactCoverage {
  state: string;
  typeScopeClosed: boolean;
  stableWindow: boolean;
  emptyResultAssessment: string;
  reasons: string[];
}

export interface ImpactResponse {
  schemaVersion: string;
  kubeatlasVersion: string;
  generatedAt: string;
  scope: { clusterId: string; mode: string; namespaceScope: string };
  sharingWarning: string;
  analysis: ImpactFacet & {
    root: ImpactIdentity;
    relation: ImpactRelation;
    maxDepth: number;
    limit: number;
    referenceEvidence: Array<{
      from: string;
      to: string;
      type: string;
      status: string;
      reason?: string;
      sourceUID?: string;
      sourceResourceVersion?: string;
      uses: Array<{
        fieldPath: string;
        mountFieldPath?: string;
        mode: string;
        optional: string;
        refresh: string;
      }>;
    }>;
    authorization: ImpactFacet & {
      applicable: boolean;
      maxDepth: number;
      limit: number;
      reason?: string;
      effectivePermissions: string;
      sourceVersionEvidence: string;
    };
    observation?: { ordinary: ImpactCoverage; authorization: ImpactCoverage };
    availability?: {
      currentGraphRead: string;
      evidence: {
        capturedAt: string;
        storage: { state: string; durabilityKnown: boolean; durable: boolean };
        history: {
          state: string;
          reasons: string[];
          coverage: string;
          retainedData: string;
          retentionEvidence?: {
            state: string;
            checkedAt: string;
            stale: boolean;
            stopped: boolean;
            bounds: {
              from: string;
              to: string;
              firstEventAt: string;
              lastEventAt: string;
              latestMarkerAt: string;
            };
          };
        };
      };
    };
  };
}
