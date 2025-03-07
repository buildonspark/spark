import { useQuery } from "@tanstack/react-query";
import useWalletStore from "../store/wallet";

export function usePaginatedTransfers(pageParams: {
  limit: number;
  offset: number;
}) {
  const getAllTransfers = useWalletStore((state) => state.getAllTransfers);
  const isInitialized = useWalletStore((state) => state.isInitialized);

  return useQuery({
    queryKey: ["wallet", "transfers", pageParams],
    queryFn: () => getAllTransfers(pageParams.limit, pageParams.offset),
    enabled: isInitialized,
    staleTime: 5000,
  });
}
