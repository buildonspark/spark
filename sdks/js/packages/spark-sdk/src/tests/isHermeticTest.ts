export const isHermeticTest = Boolean(
  typeof process !== "undefined" && process?.env?.SPARK_LOCAL_INGRESS_HOST,
);
