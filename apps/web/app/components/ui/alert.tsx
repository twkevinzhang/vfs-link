import * as React from 'react';

import { cn } from '../../lib/utils';

export function Alert({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="alert"
      className={cn(
        'grid gap-2 rounded-lg border border-border bg-white p-4 text-sm',
        className
      )}
      {...props}
    />
  );
}
