import type { SparkContext, StepCallback } from "./context";

export interface EngineStep {
  (context: SparkContext, callback: StepCallback): void;
}

export type StepActionCreator = (params?: any) => EngineStep;

export interface StepActionConfig {
  [key: string]: any;
}
