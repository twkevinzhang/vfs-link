import { FlatObject } from '@site/models';

export type Entity = FlatObject & {
  key: string;
  leaf: boolean;
  loading: boolean;
  label: string;
  children?: Entity[];
};
