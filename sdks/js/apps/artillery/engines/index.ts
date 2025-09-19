import { WalletActions } from "./wallet-actions";
import { TokenActions } from "./token-actions";
import { beforeTest, beforeScenario, afterScenario, afterTest } from "./hooks";

import type {
  ArtilleryScript,
  ArtilleryEventEmitter,
  ArtilleryHelpers,
  StepActionConfig,
  EngineStep,
  SparkContext,
} from "./types";
import { TransferActions } from "./transfer";

export class SparkEngine {
  private script: ArtilleryScript;
  private walletActions: WalletActions;
  private tokenActions: TokenActions;
  private transferActions: TransferActions;

  private scenarioEE: ArtilleryEventEmitter | null = null;

  private readonly stepActions = {
    initializePools: (params?: any) =>
      this.walletActions.initializePools(params),
    cleanupPools: () => this.walletActions.cleanupPools(),
    unlockPoolWallets: (params?: any) =>
      this.walletActions.unlockPoolWallets(params),

    announceToken: (params?: any) => this.tokenActions.announceToken(params),
    announceTokensForPool: (params?: any) =>
      this.tokenActions.announceTokensForPool(params),
    lockWallets: (params?: any) => this.walletActions.lockWallets(params),
    unlockWallets: (params?: any) => this.walletActions.unlockWallets(params),

    mintToken: (params?: any) => this.tokenActions.mintToken(params),
    transferToken: (params?: any) => this.tokenActions.transferToken(params),

    fundWalletPool: (params?: any) => this.walletActions.fundWalletPool(params),

    getBalance: (params?: any) => this.walletActions.getBalance(params),
    claimTransfer: (params?: any) => this.transferActions.claimTransfer(params),
    transfer: (params?: any) => this.transferActions.transfer(params),
    getStaticAddress: (params?: any) =>
      this.walletActions.getStaticAddress(params),
    printWalletInfo: (params?: any) =>
      this.walletActions.printWalletInfo(params),
    claimStaticDeposit: (params?: any) =>
      this.walletActions.claimStaticDeposit(params),
    withdraw: (params?: any) => this.walletActions.withdraw(params),
    queryNodes: (params?: any) => this.walletActions.queryNodes(params),
    queryPendingTransfers: (params?: any) =>
      this.walletActions.queryPendingTransfers(params),
    subscribeToEvents: (params: any) =>
      this.walletActions.subscribeToEvents(params),
    generateDepositAddress: (params?: any) =>
      this.walletActions.generateDepositAddress(params),
    queryAllTransfers: (params?: any) =>
      this.walletActions.queryAllTransfers(params),
  } as const;

  public readonly availableActions = Object.keys(this.stepActions) as Array<
    keyof typeof this.stepActions
  >;

  private initializationPromise: Promise<void> | null = null;

  constructor(
    script: ArtilleryScript,
    ee: ArtilleryEventEmitter,
    _helpers: ArtilleryHelpers,
  ) {
    this.script = script;

    this.walletActions = new WalletActions(ee, this);
    this.tokenActions = new TokenActions(ee, this);
    this.transferActions = new TransferActions(ee, this);

    this.initializationPromise = this.processBeforeTestActions();
  }

  private async processBeforeTestActions() {
    const beforeTestActions =
      (this.script as any).config?.beforeTest ||
      (this.script as any).beforeTest ||
      [];
    if (beforeTestActions.length > 0) {
      for (const action of beforeTestActions) {
        const actionKeys = Object.keys(action);
        if (actionKeys.length === 0) continue;

        const actionType = actionKeys[0];
        const actionParams = action[actionType];

        if (actionType === "think") {
          const delay = actionParams * 1000;
          await new Promise((resolve) => setTimeout(resolve, delay));
          continue;
        }

        const actionCreator =
          this.stepActions[actionType as keyof typeof this.stepActions];
        if (actionCreator) {
          const actionStep = actionCreator(actionParams);
          await new Promise((resolve, reject) => {
            actionStep({} as SparkContext, (error: any) => {
              if (error) {
                reject(error);
              } else {
                resolve(null);
              }
            });
          });
        }
      }
    }
  }

  createScenario(scenarioSpec: any, ee: ArtilleryEventEmitter) {
    return (initialContext: SparkContext, callback: any) => {
      initialContext.vars = initialContext.vars || {};

      // Store the scenario event emitter for metrics
      this.scenarioEE = ee;

      // Merge script-level variables into context
      if (this.script.config?.variables) {
        Object.assign(initialContext.vars, this.script.config.variables);
      }

      ee.emit("started");

      // Define all functions before use
      const executeMainFlow = (context: SparkContext) => {
        const steps = scenarioSpec.flow.map((step: any) =>
          this.createStep(step),
        );
        let currentStepIndex = 0;

        const executeAfterSteps = (context: SparkContext) => {
          const afterSteps = scenarioSpec.after.map((step: any) =>
            this.createStep(step),
          );
          let afterStepIndex = 0;

          const executeAfterStep = (ctx: SparkContext): void => {
            if (afterStepIndex >= afterSteps.length) {
              return callback(null, ctx);
            }

            const currentAfterStep = afterSteps[afterStepIndex];
            afterStepIndex++;

            try {
              currentAfterStep(
                ctx,
                (error: any, updatedContext?: SparkContext) => {
                  if (error) {
                    return callback(error);
                  }
                  executeAfterStep(updatedContext || ctx);
                },
              );
            } catch (error) {
              callback(error);
            }
          };

          executeAfterStep(context);
        };

        const executeNextStep = (ctx: SparkContext): void => {
          if (currentStepIndex >= steps.length) {
            if (scenarioSpec.after && Array.isArray(scenarioSpec.after)) {
              executeAfterSteps(ctx);
            } else {
              return callback(null, ctx);
            }
            return;
          }

          const currentStep = steps[currentStepIndex];
          currentStepIndex++;

          try {
            currentStep(ctx, (error: any, updatedContext?: SparkContext) => {
              if (error) {
                return callback(error);
              }

              executeNextStep(updatedContext || ctx);
            });
          } catch (error) {
            callback(error);
          }
        };

        executeNextStep(context);
      };

      const executeScenarioBefore = (context: SparkContext) => {
        if (scenarioSpec.before && Array.isArray(scenarioSpec.before)) {
          const beforeSteps = scenarioSpec.before.map((step: any) =>
            this.createStep(step),
          );
          let beforeStepIndex = 0;

          const executeBeforeStep = (ctx: SparkContext): void => {
            if (beforeStepIndex >= beforeSteps.length) {
              executeMainFlow(ctx);
              return;
            }

            const currentBeforeStep = beforeSteps[beforeStepIndex];
            beforeStepIndex++;

            try {
              currentBeforeStep(
                ctx,
                (error: any, updatedContext?: SparkContext) => {
                  if (error) {
                    return callback(error);
                  }
                  executeBeforeStep(updatedContext || ctx);
                },
              );
            } catch (error) {
              callback(error);
            }
          };

          executeBeforeStep(context);
        } else {
          executeMainFlow(context);
        }
      };

      const proceedWithScenario = () => {
        const runGlobalBefore =
          !(this.script as any).__globalBeforeExecuted &&
          (this.script as any).before;

        if (runGlobalBefore) {
          (this.script as any).__globalBeforeExecuted = true;
          const globalBeforeSteps = (this.script as any).before.map(
            (step: any) => this.createStep(step),
          );

          let globalStepIndex = 0;
          const executeGlobalStep = (ctx: SparkContext): void => {
            if (globalStepIndex >= globalBeforeSteps.length) {
              executeScenarioBefore(ctx);
              return;
            }

            const currentStep = globalBeforeSteps[globalStepIndex];
            globalStepIndex++;

            try {
              currentStep(ctx, (error: any, updatedContext?: SparkContext) => {
                if (error) {
                  return callback(error);
                }
                executeGlobalStep(updatedContext || ctx);
              });
            } catch (error) {
              callback(error);
            }
          };

          executeGlobalStep(initialContext);
        } else {
          executeScenarioBefore(initialContext);
        }
      };

      // Start execution
      if (this.initializationPromise) {
        this.initializationPromise
          .then(() => {
            proceedWithScenario();
          })
          .catch((error) => {
            console.error("Initialization failed:", error);
            callback(error);
          });
      } else {
        proceedWithScenario();
      }
    };
  }

  private createStep(requestSpec: StepActionConfig): EngineStep {
    const actionKeys = Object.keys(requestSpec);
    if (actionKeys.length === 0) {
      throw new Error("Empty step configuration provided");
    }

    const actionType = actionKeys[0];
    const actionParams = requestSpec[actionType];

    if (actionType === "think") {
      return this.createThinkStep(actionParams);
    }

    if (actionType === "loop") {
      return this.createLoopStep(actionParams);
    }

    const actionCreator =
      this.stepActions[actionType as keyof typeof this.stepActions];
    if (!actionCreator) {
      throw new Error(
        `Unknown action: ${actionType}. Available actions: ${this.availableActions.join(", ")}`,
      );
    }

    return actionCreator(actionParams);
  }

  private createThinkStep(delay: number): EngineStep {
    return (context: SparkContext, callback) => {
      const delayMs = delay * 1000;
      setTimeout(() => callback(null, context), delayMs);
    };
  }

  private processTemplateString(
    template: string,
    context: SparkContext,
  ): string {
    if (typeof template !== "string") return template;

    return template.replace(/\{\{\s*([^}]+)\s*\}\}/g, (match, expression) => {
      if (expression.startsWith("$")) {
        const varName = expression.trim();
        const value = context.vars?.[varName];
        return value !== undefined ? String(value) : match;
      }

      try {
        const evalContext = {
          ...context.vars,
          $loopCount: context.vars?.$loopCount || 1,
          $loopElement: context.vars?.$loopElement,
        };

        const functionBody = `
          ${Object.entries(evalContext)
            .map(([key, value]) => {
              if (typeof value === "bigint") {
                return `const ${key} = ${value}n;`;
              } else if (typeof value === "string") {
                return `const ${key} = ${JSON.stringify(value)};`;
              } else if (typeof value === "object" && value !== null) {
                return `const ${key} = ${JSON.stringify(value)};`;
              } else {
                return `const ${key} = ${value};`;
              }
            })
            .join("\n")}
          return ${expression};
        `;

        const result = new Function(functionBody)();
        return String(result);
      } catch (e) {
        console.warn(
          `Failed to evaluate template expression: ${expression}`,
          e,
        );
        return match;
      }
    });
  }

  private processTemplateObject(obj: any, context: SparkContext): any {
    if (typeof obj === "string") {
      return this.processTemplateString(obj, context);
    }

    if (Array.isArray(obj)) {
      return obj.map((item) => this.processTemplateObject(item, context));
    }

    if (obj && typeof obj === "object") {
      const processed: any = {};
      for (const key in obj) {
        processed[key] = this.processTemplateObject(obj[key], context);
      }
      return processed;
    }

    return obj;
  }

  private createLoopStep(loopParams: any): EngineStep {
    return (context: SparkContext, callback) => {
      const { count, over, actions, whileTrue } = loopParams;

      if (!actions || !Array.isArray(actions)) {
        return callback(new Error("Loop must have an 'actions' array"));
      }

      if (count !== undefined) {
        let loopIndex = 0;

        const executeLoopIteration = () => {
          if (loopIndex >= count) {
            return callback(null, context);
          }

          context.vars = context.vars || {};
          context.vars.$loopCount = loopIndex + 1;

          const processedActions = actions.map((action) =>
            this.processTemplateObject(action, context),
          );
          const iterationSteps = processedActions.map((action: any) =>
            this.createStep(action),
          );

          let actionIndex = 0;

          const executeNextAction = (ctx: SparkContext): void => {
            if (actionIndex >= iterationSteps.length) {
              loopIndex++;
              actionIndex = 0;
              executeLoopIteration();
              return;
            }

            const currentAction = iterationSteps[actionIndex];
            actionIndex++;

            currentAction(ctx, (error: any, updatedContext?: SparkContext) => {
              if (error) {
                return callback(error);
              }
              executeNextAction(updatedContext || ctx);
            });
          };

          executeNextAction(context);
        };

        executeLoopIteration();
      } else if (over !== undefined) {
        const items = Array.isArray(over) ? over : context.vars?.[over] || [];
        let itemIndex = 0;

        const executeLoopIteration = () => {
          if (itemIndex >= items.length) {
            return callback(null, context);
          }

          context.vars = context.vars || {};
          context.vars.$loopElement = items[itemIndex];
          context.vars.$loopCount = itemIndex + 1;

          const processedActions = actions.map((action) =>
            this.processTemplateObject(action, context),
          );
          const iterationSteps = processedActions.map((action: any) =>
            this.createStep(action),
          );

          let actionIndex = 0;

          const executeNextAction = (ctx: SparkContext): void => {
            if (actionIndex >= iterationSteps.length) {
              itemIndex++;
              actionIndex = 0;
              executeLoopIteration();
              return;
            }

            const currentAction = iterationSteps[actionIndex];
            actionIndex++;

            currentAction(ctx, (error: any, updatedContext?: SparkContext) => {
              if (error) {
                return callback(error);
              }
              executeNextAction(updatedContext || ctx);
            });
          };

          executeNextAction(context);
        };

        executeLoopIteration();
      } else if (whileTrue !== undefined) {
        return callback(new Error("whileTrue loops are not yet implemented"));
      } else {
        const executeLoopIteration = () => {
          const processedActions = actions.map((action) =>
            this.processTemplateObject(action, context),
          );
          const iterationSteps = processedActions.map((action: any) =>
            this.createStep(action),
          );

          let actionIndex = 0;

          const executeNextAction = (ctx: SparkContext): void => {
            if (actionIndex >= iterationSteps.length) {
              actionIndex = 0;
              setImmediate(() => executeLoopIteration());
              return;
            }

            const currentAction = iterationSteps[actionIndex];
            actionIndex++;

            currentAction(ctx, (error: any, updatedContext?: SparkContext) => {
              if (error) {
                return callback(error);
              }
              executeNextAction(updatedContext || ctx);
            });
          };

          executeNextAction(context);
        };

        executeLoopIteration();
      }
    };
  }
}

export type StepActionType = keyof SparkEngine["stepActions"];

let globalEE: ArtilleryEventEmitter | null = null;

function createSparkEngine(
  script: ArtilleryScript,
  ee: ArtilleryEventEmitter,
  helpers: ArtilleryHelpers,
) {
  globalEE = ee;
  return new SparkEngine(script, ee, helpers);
}

export function getGlobalEE(): ArtilleryEventEmitter | null {
  return globalEE;
}

(createSparkEngine as any).__name = "spark";
(createSparkEngine as any).beforeTest = beforeTest;
(createSparkEngine as any).beforeScenario = beforeScenario;
(createSparkEngine as any).afterScenario = afterScenario;
(createSparkEngine as any).afterTest = afterTest;

export default createSparkEngine;
module.exports = createSparkEngine;
