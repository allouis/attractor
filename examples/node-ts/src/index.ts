export function add(a: number, b: number): number {
  return a + b;
}

export function greeting(name: string): string {
  return `hello, ${name}`;
}

if (require.main === module) {
  console.log(greeting("attractor"));
  console.log(`2 + 3 = ${add(2, 3)}`);
}
