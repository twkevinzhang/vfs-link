export type DriftActionListRequestToken = Readonly<{
  requestGeneration: number;
  mutationGeneration: number;
}>;

export function createDriftActionListResponseGuard() {
  let requestGeneration = 0;
  let mutationGeneration = 0;
  return {
    beginRequest(): DriftActionListRequestToken {
      requestGeneration += 1;
      return { requestGeneration, mutationGeneration };
    },
    markMutation() {
      mutationGeneration += 1;
    },
    isCurrent(token: DriftActionListRequestToken) {
      return (
        token.requestGeneration === requestGeneration &&
        token.mutationGeneration === mutationGeneration
      );
    },
  };
}
