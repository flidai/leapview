declare module '*.css'

declare module '*/static/vendor/datastar-1.0.2.js?v=dev' {
  export function mergePatch(...args: unknown[]): unknown
}

declare module '*settings-surfaces.js' {
  export function setDatastarLitRuntimeForTests(runtime: unknown): void
}

declare module 'p5/accessibility' {
  const addon: Function
  export default addon
}

declare module 'p5/color' {
  const addon: Function
  export default addon
}

declare module 'p5/core' {
  import p5 from 'p5'
  export default p5
}

declare module 'p5/math' {
  const addon: Function
  export default addon
}

declare module 'p5/shape' {
  const addon: Function
  export default addon
}

declare module '*.topology.js' {
  type TopologyOptions = {
    el: HTMLElement
    p5: typeof import('p5').default
    color: string
    backgroundColor: string
    mouseControls: boolean
    touchControls: boolean
    gyroControls: boolean
    minHeight: number
    minWidth: number
    scale: number
    scaleMobile: number
  }

  const topology: (options: TopologyOptions) => { destroy(): void }
  export default topology
}
