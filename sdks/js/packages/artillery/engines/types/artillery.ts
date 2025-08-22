export interface ArtilleryScript {
  config: {
    target: string;
    phases: Array<{
      duration: number;
      arrivalRate: number;
      rampTo?: number;
      name?: string;
    }>;
    engines: Record<string, any>;
    variables?: Record<string, any>;
  };
  scenarios: Array<{
    name: string;
    weight?: number;
    engine: string;
    flow: Array<Record<string, any>>;
  }>;
}

export interface ArtilleryEventEmitter {
  emit(event: string, ...args: any[]): void;
  on(event: string, callback: (...args: any[]) => void): void;
  removeListener?(event: string, callback: (...args: any[]) => void): void;
}

export interface ArtilleryHelpers {
  [key: string]: any;
}
