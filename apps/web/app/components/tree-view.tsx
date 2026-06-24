import { ChevronRight, File, Folder, FolderOpen } from 'lucide-react';

import { Button } from './ui/button';
import { normalizePath } from '../lib/format';
import { cn } from '../lib/utils';
import { TreeNode } from '../types/files';

type TreeViewProps = {
  node?: TreeNode;
  currentPath: string;
  onSelectPath: (path: string) => void;
};

export function TreeView({ node, currentPath, onSelectPath }: TreeViewProps) {
  if (!node) {
    return null;
  }

  return (
    <nav aria-label="資料夾樹" className="grid gap-1">
      <TreeItem
        node={node}
        currentPath={normalizePath(currentPath)}
        onSelectPath={onSelectPath}
        depth={0}
      />
    </nav>
  );
}

function TreeItem({
  node,
  currentPath,
  onSelectPath,
  depth,
}: {
  node: TreeNode;
  currentPath: string;
  onSelectPath: (path: string) => void;
  depth: number;
}) {
  const path = normalizePath(node.path);
  const isSelected = path === currentPath;
  const children = node.children ?? [];
  const isDirectory = node.kind === 'directory';
  const hasChildren = children.length > 0;
  const Icon = !isDirectory ? File : isSelected ? FolderOpen : Folder;

  return (
    <div className="grid gap-1">
      <Button
        variant={isSelected ? 'secondary' : 'ghost'}
        size="sm"
        className={cn(
          'h-8 w-full justify-start overflow-hidden px-2 text-left',
          isSelected && 'font-semibold'
        )}
        style={{ paddingLeft: `${Math.min(depth * 14 + 8, 56)}px` }}
        onClick={() => onSelectPath(path)}
        title={path}
      >
        {isDirectory ? (
          <ChevronRight
            aria-hidden="true"
            className={cn(
              'h-3.5 w-3.5 shrink-0 text-muted-foreground',
              !hasChildren && 'opacity-0'
            )}
          />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span className="truncate">{node.name || '/'}</span>
      </Button>
      {hasChildren && (
        <div className="grid gap-1">
          {children.map((child) => (
            <TreeItem
              key={child.path}
              node={child}
              currentPath={currentPath}
              onSelectPath={onSelectPath}
              depth={depth + 1}
            />
          ))}
        </div>
      )}
    </div>
  );
}
