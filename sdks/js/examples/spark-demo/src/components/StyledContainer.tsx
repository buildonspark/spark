import styled from "styled-components";

export default function StyledContainer({
  children,
  isPressable = false,
  onClick,
  className,
}: {
  children: React.ReactNode;
  isPressable?: boolean;
  className?: string;
  onClick?: () => void;
}) {
  return isPressable ? (
    <ButtonContainer className={className} onClick={onClick}>
      {children}
    </ButtonContainer>
  ) : (
    <DivContainer className={className}>{children}</DivContainer>
  );
}

const DivContainer = styled.div`
  background: rgba(250, 250, 250, 0.06);
  border-radius: 8px;
`;

const ButtonContainer = styled.button`
  width: 100%;
  background: rgba(250, 250, 250, 0.06);
  border-radius: 8px;
  backdrop-filter: blur(20px);
  box-shadow: 0px 4px 24px rgba(0, 0, 0, 0.25);
`;
