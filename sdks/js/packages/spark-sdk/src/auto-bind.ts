// Gets all non-builtin properties up the prototype chain.
function getAllProperties(object: object): Set<[object, PropertyKey]> {
  const properties = new Set<[object, PropertyKey]>();
  let current: object | null = object;

  while (current && current !== Object.prototype) {
    for (const key of Reflect.ownKeys(current)) {
      properties.add([current, key]);
    }
    current = Reflect.getPrototypeOf(current);
  }

  return properties;
}

type Pattern = string | RegExp;

interface AutoBindOptions {
  include?: Pattern[];
  exclude?: Pattern[];
}

export default function autoBind<T extends object>(
  self: T,
  { include, exclude }: AutoBindOptions = {},
): T {
  // Filter function converts key to a string for matching purposes.
  const filter = (key: PropertyKey): boolean => {
    const keyStr = typeof key === "string" ? key : key.toString();
    const match = (pattern: Pattern) =>
      typeof pattern === "string" ? keyStr === pattern : pattern.test(keyStr);

    if (include) {
      return include.some(match);
    }

    if (exclude) {
      return !exclude.some(match);
    }

    return true;
  };

  // Iterate over all properties from the prototype chain.
  for (const [proto, key] of getAllProperties(self.constructor.prototype)) {
    // Skip the constructor and any keys that don't pass the filter.
    if (key === "constructor" || !filter(key)) {
      continue;
    }

    const descriptor = Reflect.getOwnPropertyDescriptor(proto, key);
    if (descriptor && typeof descriptor.value === "function") {
      // Bind the method to 'self'. We use a type assertion because
      // we're dynamically accessing properties.
      (self as any)[key] = (self as any)[key].bind(self);
    }
  }

  return self;
}
