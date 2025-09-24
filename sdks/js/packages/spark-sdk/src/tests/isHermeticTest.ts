export const isHermeticTest = Boolean(
  typeof process !== "undefined" && process?.env?.MINIKUBE_IP,
);
