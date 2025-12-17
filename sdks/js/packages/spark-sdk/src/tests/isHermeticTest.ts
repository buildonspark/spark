export const isHermeticTest = Boolean(
  typeof process !== "undefined" && process?.env?.MINIKUBE_IP,
);

export const testHermeticOnly = !isHermeticTest ? it.skip : it;
export const testHermeticAndLocalOnly = process.env.GITHUB_ACTIONS || !isHermeticTest ? it.skip : it;
