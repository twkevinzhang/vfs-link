export type Pagination = {
  limit: number;
  offset: number;
  total: number;
  query: string;
  hasNext: boolean;
  hasPrev: boolean;
};
