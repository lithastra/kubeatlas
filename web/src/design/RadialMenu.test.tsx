import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';

import { RadialMenu, type RadialMenuOption } from './RadialMenu';

function Harness({ options }: { options: RadialMenuOption[] }) {
  const [open, setOpen] = useState(true);
  return (
    <RadialMenu
      open={open}
      anchor={{ x: 200, y: 200 }}
      options={options}
      onClose={() => setOpen(false)}
      label="Test menu"
    />
  );
}

describe('RadialMenu', () => {
  it('renders one menuitem per option and exposes role=menu', () => {
    const opts: RadialMenuOption[] = [
      { id: 'a', label: 'A', onSelect: () => {} },
      { id: 'b', label: 'B', onSelect: () => {} },
      { id: 'c', label: 'C', onSelect: () => {} },
    ];
    render(<Harness options={opts} />);
    expect(screen.getByRole('menu', { name: 'Test menu' })).toBeInTheDocument();
    expect(screen.getAllByRole('menuitem')).toHaveLength(3);
  });

  it('closed state renders nothing', () => {
    render(
      <RadialMenu
        open={false}
        anchor={{ x: 0, y: 0 }}
        options={[{ id: 'x', label: 'X', onSelect: () => {} }]}
        onClose={() => {}}
      />,
    );
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('arrow keys advance focus and Enter activates', () => {
    const onSelectB = jest.fn();
    const opts: RadialMenuOption[] = [
      { id: 'a', label: 'A', onSelect: () => {} },
      { id: 'b', label: 'B', onSelect: onSelectB },
      { id: 'c', label: 'C', onSelect: () => {} },
    ];
    render(<Harness options={opts} />);
    const menu = screen.getByRole('menu');
    // Initial focus is on the first item (no active option).
    fireEvent.keyDown(menu, { key: 'ArrowRight' });
    fireEvent.keyDown(menu, { key: 'Enter' });
    expect(onSelectB).toHaveBeenCalledTimes(1);
  });

  it('opens with focus on the active option when one is marked', () => {
    const opts: RadialMenuOption[] = [
      { id: 'a', label: 'A', onSelect: () => {} },
      { id: 'b', label: 'B', onSelect: () => {}, active: true },
      { id: 'c', label: 'C', onSelect: () => {} },
    ];
    render(<Harness options={opts} />);
    // Focused = the active option (B).
    expect(screen.getByRole('menuitem', { name: 'B' })).toHaveFocus();
  });

  it('Esc fires onClose', () => {
    const onClose = jest.fn();
    render(
      <RadialMenu
        open
        anchor={{ x: 0, y: 0 }}
        options={[{ id: 'x', label: 'X', onSelect: () => {} }]}
        onClose={onClose}
      />,
    );
    fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
