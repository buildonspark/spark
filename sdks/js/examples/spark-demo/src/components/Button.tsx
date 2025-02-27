import styled, { css } from "styled-components";
import { LoadingSpinner } from "./LoadingSpinner";

interface ButtonsProps {
  direction?: "horizontal" | "vertical";
  kind?: "primary" | "secondary";
  icon?: React.ReactNode;
  text?: string;
  height?: number;
  disabled?: boolean;
  opaque?: boolean;
  loading?: boolean;
  onClick?: () => void;
}

export default function Button({
  direction = "horizontal",
  kind = "primary",
  icon,
  text,
  height = 64,
  onClick,
  disabled,
  loading = false,
  opaque,
}: ButtonsProps) {
  return (
    <StyledButton
      direction={direction}
      kind={kind}
      onClick={loading ? undefined : onClick} // disable click if loading
      disabled={disabled}
      opaque={opaque}
      height={height}
    >
      {loading ? (
        <LoadingSpinner size={20} />
      ) : (
        <>
          {icon}
          {text}
        </>
      )}
    </StyledButton>
  );
}

const StyledButton = styled.button<{
  direction: "horizontal" | "vertical";
  kind: "primary" | "secondary";
  disabled?: boolean;
  opaque?: boolean;
  height?: number;
}>`
  width: 100%;
  height: ${({ height }) => height}px;
  display: flex;
  align-items: center;
  justify-content: center;

  padding: 5px 0px;
  gap: 12px;
  border: 1px solid rgba(249, 249, 249, 0.1);
  border-radius: 8px;
  font-family: "Inter";

  font-size: 15px;
  line-height: 38px;

  text-align: center;
  margin-top: 10px;

  ${({ disabled }) =>
    disabled &&
    css`
      opacity: 0.5;
    `}

  ${({ kind, opaque }) =>
    kind === "primary"
      ? css`
          background: #fafafa;
          color: #0a0a0a;
        `
      : css`
          background: #1a1a1a;
        `}

  ${({ direction }) =>
    direction === "vertical" &&
    css`
      border-radius: 8px;
      flex-direction: column;
      gap: 0px;
      line-height: 24px;
    `}

  ${({ opaque }) =>
    opaque &&
    css`
      &:hover {
        background: linear-gradient(
          180deg,
          #0e3154 ${opaque ? "100%" : "0%"},
          rgba(14, 49, 84, 0.5) 100%
        );
      }
    `}
  

  ${({ disabled, opaque }) =>
    !disabled &&
    css`
      &:active {
        background: linear-gradient(
          180deg,
          #0a253b ${opaque ? "100%" : "0%"},
          rgba(10, 37, 59, 0.5) 100%
        );
        transform: scale(0.98);
      }
    `}
`;
