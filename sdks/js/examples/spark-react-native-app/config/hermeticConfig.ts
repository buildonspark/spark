import type { ConfigOptions } from '@buildonspark/spark-sdk';

/**
 * Minikube operator configuration for hermetic Android tests.
 * Values sourced from spark-sdk wallet-config.ts (local/hermetic config).
 * CI overwrites sparkEnv.ts to enable this config at build time.
 */
export const HERMETIC_CONFIG: ConfigOptions = {
  network: 'LOCAL',
  signingOperators: {
    '0000000000000000000000000000000000000000000000000000000000000001': {
      id: 0,
      identifier:
        '0000000000000000000000000000000000000000000000000000000000000001',
      address: 'https://0.spark-web.minikube.local',
      identityPublicKey:
        '0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510',
    },
    '0000000000000000000000000000000000000000000000000000000000000002': {
      id: 1,
      identifier:
        '0000000000000000000000000000000000000000000000000000000000000002',
      address: 'https://1.spark-web.minikube.local',
      identityPublicKey:
        '0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27',
    },
    '0000000000000000000000000000000000000000000000000000000000000003': {
      id: 2,
      identifier:
        '0000000000000000000000000000000000000000000000000000000000000003',
      address: 'https://2.spark-web.minikube.local',
      identityPublicKey:
        '0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6',
    },
  },
  coordinatorIdentifier:
    '0000000000000000000000000000000000000000000000000000000000000001',
  threshold: 2,
  electrsUrl: 'http://mempool.minikube.local/api',
  sspClientOptions: {
    baseUrl: 'http://app.minikube.local',
    identityPublicKey:
      '028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4',
    schemaEndpoint: 'graphql/spark/rc',
  },
};
