/**
 * Shared types for all stores
 */

/** Base state for all collection stores */
export interface CollectionState<T> {
  items: T[];
  loading: boolean;
  error: string | null;
  lastUpdated: Date | null;
  /** Whether real-time subscription is active */
  subscribed: boolean;
}

/** Pagination metadata */
export interface PaginationState {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
}

/** State with pagination support */
export interface PaginatedState<T> extends CollectionState<T> {
  pagination: PaginationState;
  page?: number;
  perPage?: number;
  totalItems?: number;
  totalPages?: number;
}

/** Filter options for queries */
export interface FilterOptions {
  search?: string;
  sortField?: string;
  sortOrder?: "asc" | "desc";
  filter?: string;
}

/** Result of a store action */
export interface ActionResult<T = void> {
  success: boolean;
  data?: T;
  error?: string;
}
